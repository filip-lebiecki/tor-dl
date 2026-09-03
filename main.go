package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
)

//go:embed static
var static embed.FS

type FileJSON struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type QueueItem struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	MagnetURI  string     `json:"magnetURI"`
	State      string     `json:"state"`
	Progress   float64    `json:"progress"`
	TotalBytes int64      `json:"totalBytes"`
	DoneBytes  int64      `json:"doneBytes"`
	Speed      int64      `json:"speed"`
	AddedAt    time.Time  `json:"addedAt"`
	Files      []FileJSON `json:"files"`

	t           *torrent.Torrent
	autoRemove  bool
	completedAt time.Time
	prevRead    int64
	prevTime    time.Time
}

const autoRemoveGracePeriod = 3 * time.Second

type persistedItem struct {
	MagnetURI  string    `json:"magnetURI"`
	AutoRemove bool      `json:"autoRemove"`
	AddedAt    time.Time `json:"addedAt"`
}

type App struct {
	client  *torrent.Client
	mu      sync.Mutex
	queue   []*QueueItem
	dataDir string
}

func main() {
	port := flag.Int("port", 8080, "port to listen on")
	flag.Parse()

	dataDir := filepath.Join(".", "downloads")
	os.MkdirAll(dataDir, 0755)

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dataDir

	client, err := torrent.NewClient(cfg)
	if err != nil {
		log.Fatal(err)
	}

	app := &App{
		client:  client,
		queue:   make([]*QueueItem, 0),
		dataDir: dataDir,
	}

	app.loadQueue()
	go app.statsLoop()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/add", app.handleAdd)
	mux.HandleFunc("GET /api/queue", app.handleQueue)
	mux.HandleFunc("DELETE /api/remove/{id}", app.handleRemove)

	staticSub, _ := fs.Sub(static, "static")
	mux.Handle("/", http.FileServer(http.FS(staticSub)))
	mux.Handle("/downloads/", http.StripPrefix("/downloads/", http.FileServer(http.Dir(app.dataDir))))

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Listening on http://localhost:%d", *port)
	log.Println("Downloads saved to:", dataDir)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func (app *App) handleAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Magnet     string `json:"magnet"`
		AutoRemove bool   `json:"autoRemove"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Magnet == "" {
		http.Error(w, "magnet link is required", http.StatusBadRequest)
		return
	}

	item, err := app.addMagnet(body.Magnet, body.AutoRemove)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(item.public())
}

func (app *App) handleQueue(w http.ResponseWriter, r *http.Request) {
	app.mu.Lock()
	items := make([]*QueueItem, len(app.queue))
	copy(items, app.queue)
	app.mu.Unlock()

	result := make([]publicItem, len(items))
	for i, item := range items {
		result[i] = item.public()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (app *App) handleRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	if err := app.remove(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type publicItem struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	MagnetURI  string     `json:"magnetURI"`
	State      string     `json:"state"`
	Progress   float64    `json:"progress"`
	TotalBytes int64      `json:"totalBytes"`
	DoneBytes  int64      `json:"doneBytes"`
	Speed      int64      `json:"speed"`
	AddedAt    time.Time  `json:"addedAt"`
	Files      []FileJSON `json:"files"`
}

func (qi *QueueItem) public() publicItem {
	return publicItem{
		ID:         qi.ID,
		Name:       qi.Name,
		MagnetURI:  qi.MagnetURI,
		State:      qi.State,
		Progress:   qi.Progress,
		TotalBytes: qi.TotalBytes,
		DoneBytes:  qi.DoneBytes,
		Speed:      qi.Speed,
		AddedAt:    qi.AddedAt,
		Files:      qi.Files,
	}
}

func (app *App) addMagnet(magnetURI string, autoRemove bool) (*QueueItem, error) {
	return app.addMagnetAt(magnetURI, autoRemove, time.Now())
}

func (app *App) addMagnetAt(magnetURI string, autoRemove bool, addedAt time.Time) (*QueueItem, error) {
	t, err := app.client.AddMagnet(magnetURI)
	if err != nil {
		return nil, err
	}

	id := t.InfoHash().HexString()

	app.mu.Lock()
	defer app.mu.Unlock()

	for _, existing := range app.queue {
		if existing.ID == id {
			existing.autoRemove = autoRemove
			app.saveQueueLocked()
			return existing, nil
		}
	}

	item := &QueueItem{
		ID:         id,
		MagnetURI:  magnetURI,
		Name:       t.Name(),
		State:      "waiting",
		AddedAt:    addedAt,
		t:          t,
		autoRemove: autoRemove,
	}

	app.queue = append(app.queue, item)

	hasActive := false
	for _, q := range app.queue {
		if q.State == "downloading" || q.State == "fetching" {
			hasActive = true
			break
		}
	}

	if !hasActive {
		app.startItem(item)
	}
	app.saveQueueLocked()

	return item, nil
}

func (app *App) startItem(item *QueueItem) {
	if item.t.Info() != nil {
		item.State = "downloading"
		item.Name = item.t.Info().Name
		item.TotalBytes = item.t.Length()
		for _, f := range item.t.Files() {
			item.Files = append(item.Files, FileJSON{
				Path: f.DisplayPath(),
				Size: f.Length(),
			})
		}
		item.t.DownloadAll()
	} else {
		item.State = "fetching"
		go func() {
			<-item.t.GotInfo()
			app.mu.Lock()
			defer app.mu.Unlock()
			if item.State != "fetching" {
				return
			}
			if item.t.Info() != nil {
				item.Name = item.t.Info().Name
				item.TotalBytes = item.t.Length()
				for _, f := range item.t.Files() {
					item.Files = append(item.Files, FileJSON{
						Path: f.DisplayPath(),
						Size: f.Length(),
					})
				}
			}
			item.State = "downloading"
			item.t.DownloadAll()
		}()
	}
}

func (app *App) remove(id string) error {
	app.mu.Lock()
	defer app.mu.Unlock()

	wasActive, ok := app.removeItemLocked(id)
	if !ok {
		return fmt.Errorf("not found")
	}
	if wasActive {
		app.startNextLocked()
	}
	app.saveQueueLocked()
	return nil
}

func (app *App) removeItemLocked(id string) (wasActive, ok bool) {
	for i, item := range app.queue {
		if item.ID == id {
			if item.t != nil {
				item.t.Drop()
			}
			wasActive = item.State == "downloading" || item.State == "fetching"
			app.queue = append(app.queue[:i], app.queue[i+1:]...)
			return wasActive, true
		}
	}
	return false, false
}

func (app *App) startNextLocked() {
	for _, item := range app.queue {
		if item.State == "waiting" {
			app.startItem(item)
			return
		}
	}
}

func (app *App) statsLoop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		app.updateStats()
	}
}

func (app *App) updateStats() {
	app.mu.Lock()
	defer app.mu.Unlock()

	now := time.Now()
	var toRemove []string

	for _, item := range app.queue {
		if item.t == nil {
			continue
		}

		if info := item.t.Info(); info != nil && item.TotalBytes == 0 {
			item.Name = info.Name
			item.TotalBytes = item.t.Length()
			if len(item.Files) == 0 {
				for _, f := range item.t.Files() {
					item.Files = append(item.Files, FileJSON{
						Path: f.DisplayPath(),
						Size: f.Length(),
					})
				}
			}
		}

		if item.State == "downloading" || item.State == "fetching" {
			done := item.t.BytesCompleted()
			item.DoneBytes = done

			if item.TotalBytes > 0 {
				item.Progress = float64(done) / float64(item.TotalBytes) * 100
				if item.Progress > 100 {
					item.Progress = 100
				}
			}

			if !item.prevTime.IsZero() {
				dt := now.Sub(item.prevTime).Seconds()
				if dt > 0 {
					delta := done - item.prevRead
					if delta < 0 {
						delta = 0
					}
					item.Speed = int64(float64(delta) / dt)
				}
			}
			item.prevRead = done
			item.prevTime = now

			if item.TotalBytes > 0 && done >= item.TotalBytes {
				item.State = "complete"
				item.Progress = 100
				item.DoneBytes = item.TotalBytes
				item.Speed = 0
				item.completedAt = now
				app.startNextLocked()
			}
		}

		if item.State == "complete" && item.autoRemove && !item.completedAt.IsZero() &&
			now.Sub(item.completedAt) >= autoRemoveGracePeriod {
			toRemove = append(toRemove, item.ID)
		}
	}

	if len(toRemove) > 0 {
		for _, id := range toRemove {
			app.removeItemLocked(id)
		}
		app.saveQueueLocked()
	}
}

func (app *App) queueFilePath() string {
	return filepath.Join(app.dataDir, ".queue.json")
}

func (app *App) saveQueueLocked() {
	items := make([]persistedItem, 0, len(app.queue))
	for _, item := range app.queue {
		items = append(items, persistedItem{
			MagnetURI:  item.MagnetURI,
			AutoRemove: item.autoRemove,
			AddedAt:    item.AddedAt,
		})
	}

	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		log.Println("save queue:", err)
		return
	}

	tmp := app.queueFilePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Println("save queue:", err)
		return
	}
	if err := os.Rename(tmp, app.queueFilePath()); err != nil {
		log.Println("save queue:", err)
	}
}

func (app *App) loadQueue() {
	data, err := os.ReadFile(app.queueFilePath())
	if err != nil {
		return
	}

	var items []persistedItem
	if err := json.Unmarshal(data, &items); err != nil {
		log.Println("load queue:", err)
		return
	}

	for _, pi := range items {
		if _, err := app.addMagnetAt(pi.MagnetURI, pi.AutoRemove, pi.AddedAt); err != nil {
			log.Println("restore magnet:", err)
		}
	}
}

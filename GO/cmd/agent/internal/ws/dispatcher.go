// Package ws gère la connexion WebSocket persistante de l'agent et le dispatch
// des messages entrants vers les handlers appropriés.
//
// Protocole (§4 ARCHITECTURE.md) :
//   - Une seule WSS persistante par agent, multiplexée par task_id
//   - Messages Serveur→Agent : exec, put_file, fetch_file, cancel
//   - Messages Agent→Serveur : ack, stdout, result
//   - Reconnexion avec backoff exponentiel (1s..60s), sauf code 4001 (révocation)
//
// Architecture interne :
//   - Dispatcher reçoit les messages JSON bruts et les route vers les handlers
//   - Chaque exec lance une goroutine indépendante (pas de blocking du read loop)
//   - taskRegistry : map[task_id]*exec.Cmd pour les cancellations SIGTERM
package ws

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// CloseCodeRevoked indique une révocation définitive — pas de reconnexion.
	CloseCodeRevoked = 4001
	// MaxConcurrentTasks est le nombre maximum de tâches exec simultanées.
	MaxConcurrentTasks = 10
	// StdoutBufferMax est la taille maximale de stdout avant troncature (5 MB).
	StdoutBufferMax = 5 * 1024 * 1024
)

// MessageHandler est la signature d'un handler de message WebSocket.
// Il reçoit le message décodé et le contexte de la connexion.
type MessageHandler interface {
	// HandleExec exécute une commande shell et envoie ack + result.
	HandleExec(ctx context.Context, msg ExecMsg, send SendFunc) error
	// HandlePutFile écrit un fichier base64 sur disque.
	HandlePutFile(ctx context.Context, msg PutFileMsg, send SendFunc) error
	// HandleFetchFile lit un fichier et retourne son contenu en base64.
	HandleFetchFile(ctx context.Context, msg FetchFileMsg, send SendFunc) error
}

// SendFunc est la fonction d'envoi de messages JSON sur le WebSocket.
type SendFunc func(payload any) error

// --- Message types (Serveur → Agent) ---

// BaseMsg est l'enveloppe commune à tous les messages.
type BaseMsg struct {
	TaskID string `json:"task_id"`
	Type   string `json:"type"`
}

// ExecMsg est le message d'exécution de commande (§4 ARCHITECTURE.md).
type ExecMsg struct {
	BaseMsg
	Cmd        string `json:"cmd"`
	Stdin      string `json:"stdin,omitempty"` // base64 | ""
	Timeout    int    `json:"timeout"`
	Become     bool   `json:"become"`
	BecomeMethod string `json:"become_method,omitempty"`
	ExpiresAt  int64  `json:"expires_at,omitempty"`
}

// PutFileMsg est le message de transfert de fichier vers l'agent (§4).
type PutFileMsg struct {
	BaseMsg
	Dest string `json:"dest"`
	Data string `json:"data"` // base64
	Mode string `json:"mode"` // ex: "0700"
}

// FetchFileMsg est le message de récupération de fichier (§4).
type FetchFileMsg struct {
	BaseMsg
	Src string `json:"src"`
}

// CancelMsg est le message d'annulation de tâche (§4).
type CancelMsg struct {
	BaseMsg
}

// --- Reconnect manager ---

// ReconnectManager gère le backoff exponentiel pour les reconnexions WebSocket.
type ReconnectManager struct {
	baseDelay float64
	maxDelay  float64
	attempt   int
}

// NewReconnectManager crée un ReconnectManager avec baseDelay et maxDelay en secondes.
func NewReconnectManager(baseDelay, maxDelay float64) *ReconnectManager {
	return &ReconnectManager{baseDelay: baseDelay, maxDelay: maxDelay}
}

// NextDelay retourne le prochain délai de reconnexion et incrémente le compteur.
func (r *ReconnectManager) NextDelay() time.Duration {
	delay := r.baseDelay * math.Pow(2, float64(r.attempt))
	if delay > r.maxDelay {
		delay = r.maxDelay
	}
	r.attempt++
	return time.Duration(delay * float64(time.Second))
}

// Reset remet le compteur à zéro après une connexion réussie.
func (r *ReconnectManager) Reset() {
	r.attempt = 0
}

// ShouldReconnect retourne false si le code de fermeture indique une révocation.
func (r *ReconnectManager) ShouldReconnect(closeCode int) bool {
	return closeCode != CloseCodeRevoked
}

// --- Connection config ---

// ConnConfig regroupe les paramètres de connexion WebSocket.
type ConnConfig struct {
	// ServerURL est l'URL WSS du relay server (wss://relay.example.com/ws/agent).
	ServerURL string
	// JWT est le token d'authentification Bearer.
	JWT string
	// CABundle est le chemin vers un CA bundle PEM custom (vide = store système).
	CABundle string
	// Insecure désactive la vérification TLS (tests uniquement).
	Insecure bool
}

// --- Dispatcher ---

// Dispatcher maintient la connexion WebSocket et route les messages.
type Dispatcher struct {
	cfg            ConnConfig
	handler        MessageHandler
	mu             sync.Mutex
	tasks          map[string]context.CancelFunc // task_id → cancel goroutine
	maxConcurrent  int
}

// NewDispatcher crée un Dispatcher avec le handler fourni.
// maxConcurrent = 0 → utilise MaxConcurrentTasks (constante, défaut 10).
func NewDispatcher(cfg ConnConfig, handler MessageHandler, maxConcurrent ...int) *Dispatcher {
	max := MaxConcurrentTasks
	if len(maxConcurrent) > 0 && maxConcurrent[0] > 0 {
		max = maxConcurrent[0]
	}
	return &Dispatcher{
		cfg:           cfg,
		handler:       handler,
		tasks:         make(map[string]context.CancelFunc),
		maxConcurrent: max,
	}
}

// Run ouvre la connexion WebSocket et entre dans la boucle de lecture.
// Tourne jusqu'à ce que ctx soit annulé ou que la connexion soit révoquée (4001).
func (d *Dispatcher) Run(ctx context.Context) error {
	reconnect := NewReconnectManager(1.0, 60.0)

	for {
		err := d.connect(ctx, reconnect)
		if err == nil {
			// Connexion fermée proprement via ctx
			return nil
		}

		// Vérification révocation
		var closeErr *websocket.CloseError
		if isClose(err, &closeErr) && !reconnect.ShouldReconnect(closeErr.Code) {
			return fmt.Errorf("ws: agent revoked by server (code %d)", closeErr.Code)
		}

		delay := reconnect.NextDelay()
		log.Printf("[WS] Connection lost: %v — reconnecting in %s", err, delay)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// connect établit une connexion WSS et entre dans la boucle de lecture.
func (d *Dispatcher) connect(ctx context.Context, reconnect *ReconnectManager) error {
	tlsCfg, err := buildTLSConfig(d.cfg.CABundle, d.cfg.Insecure)
	if err != nil {
		return fmt.Errorf("ws: build TLS config: %w", err)
	}

	dialer := websocket.Dialer{
		TLSClientConfig:  tlsCfg,
		HandshakeTimeout: 15 * time.Second,
	}

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+d.cfg.JWT)

	conn, _, err := dialer.DialContext(ctx, d.cfg.ServerURL, headers)
	if err != nil {
		return fmt.Errorf("ws: dial %s: %w", d.cfg.ServerURL, err)
	}
	defer conn.Close()

	reconnect.Reset()
	log.Printf("[WS] Connected to %s", d.cfg.ServerURL)

	// Heartbeat : répond automatiquement aux pings du serveur avec un pong.
	// gorilla/websocket envoie les pongs via le handler enregistré.
	sendMu := &sync.Mutex{}
	conn.SetPongHandler(func(appData string) error {
		log.Printf("[WS] Pong received")
		return nil
	})
	conn.SetPingHandler(func(appData string) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return conn.WriteMessage(websocket.PongMessage, []byte(appData))
	})

	send := func(payload any) error {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		sendMu.Lock()
		defer sendMu.Unlock()
		return conn.WriteMessage(websocket.TextMessage, data)
	}

	sem := make(chan struct{}, d.maxConcurrent)

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var base BaseMsg
		if err := json.Unmarshal(raw, &base); err != nil {
			log.Printf("[WS] Non-JSON message: %q", string(raw)[:min(200, len(raw))])
			continue
		}

		switch base.Type {
		case "exec":
			var msg ExecMsg
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("[WS] Bad exec message: %v", err)
				continue
			}
			select {
			case sem <- struct{}{}:
				taskCtx, cancel := context.WithCancel(ctx)
				d.registerTask(msg.TaskID, cancel)
				go func() {
					defer func() { <-sem }()
					defer d.unregisterTask(msg.TaskID)
					if err := d.handler.HandleExec(taskCtx, msg, send); err != nil {
						log.Printf("[WS] exec task %s error: %v", msg.TaskID, err)
					}
				}()
			default:
				_ = send(map[string]any{
					"task_id":   base.TaskID,
					"type":      "result",
					"rc":        -1,
					"stdout":    "",
					"stderr":    "agent_busy",
					"truncated": false,
				})
			}

		case "put_file":
			var msg PutFileMsg
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("[WS] Bad put_file message: %v", err)
				continue
			}
			go func() {
				if err := d.handler.HandlePutFile(ctx, msg, send); err != nil {
					log.Printf("[WS] put_file task %s error: %v", msg.TaskID, err)
				}
			}()

		case "fetch_file":
			var msg FetchFileMsg
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("[WS] Bad fetch_file message: %v", err)
				continue
			}
			go func() {
				if err := d.handler.HandleFetchFile(ctx, msg, send); err != nil {
					log.Printf("[WS] fetch_file task %s error: %v", msg.TaskID, err)
				}
			}()

		case "cancel":
			var msg CancelMsg
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			d.cancelTask(msg.TaskID)

		default:
			log.Printf("[WS] Unknown message type: %s (task_id=%s)", base.Type, base.TaskID)
		}
	}
}

func (d *Dispatcher) registerTask(taskID string, cancel context.CancelFunc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tasks[taskID] = cancel
}

func (d *Dispatcher) unregisterTask(taskID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.tasks, taskID)
}

func (d *Dispatcher) cancelTask(taskID string) {
	d.mu.Lock()
	cancel, ok := d.tasks[taskID]
	d.mu.Unlock()
	if ok {
		cancel()
		log.Printf("[WS] Task %s cancelled", taskID)
	} else {
		log.Printf("[WS] Cancel received for unknown task: %s", taskID)
	}
}

// buildTLSConfig construit un TLS config strict (MinVersion TLS 1.2, cert vérifié).
// caBundle vide → store système. insecure=true désactive la vérification (tests uniquement).
func buildTLSConfig(caBundle string, insecure bool) (*tls.Config, error) {
	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: insecure, //nolint:gosec // tests only, guarded by flag
	}
	if !insecure && caBundle != "" {
		pem, err := os.ReadFile(caBundle)
		if err != nil {
			return nil, fmt.Errorf("read CA bundle: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no valid certs in CA bundle %s", caBundle)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

func isClose(err error, target **websocket.CloseError) bool {
	ce, ok := err.(*websocket.CloseError)
	if ok && target != nil {
		*target = ce
	}
	return ok
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

package live

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/protocol"
)

// Límites de la cola de salida (§5.8).
const (
	maxQueueMsgs  = 256
	maxQueueBytes = 1 << 20
	// Por encima de esto no se generan parciales ni se mandan frames TTS.
	heavyQueueMsgs  = 64
	heavyQueueBytes = 512 << 10
)

type outMsg struct {
	seq    uint32
	typ    string
	binary bool
	data   []byte
	at     time.Time
}

// Conn es una conexión WebSocket. La escritura la hace una sola goroutine
// (writer) que vacía una cola acotada.
type Conn struct {
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc

	mu         sync.Mutex
	q          []outMsg
	qBytes     int
	closeCode  int
	closeWhy   string
	notify     chan struct{}
	lastActive atomic.Int64
	inflight   atomic.Int64
	onSent     func(outMsg)
}

func newConn(ws *websocket.Conn) *Conn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{ws: ws, ctx: ctx, cancel: cancel, notify: make(chan struct{}, 1)}
	c.touch()
	return c
}

func (c *Conn) touch() { c.lastActive.Store(time.Now().UnixNano()) }

func (c *Conn) idleFor() time.Duration {
	return time.Since(time.Unix(0, c.lastActive.Load()))
}

func (c *Conn) signal() {
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// enqueue agrega un mensaje; false si la cola está llena.
func (c *Conn) enqueue(m outMsg) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closeCode != 0 {
		return true // se está cerrando: se descarta en silencio (queda en el buffer de reenvío)
	}
	if len(c.q) >= maxQueueMsgs || c.qBytes+len(m.data) > maxQueueBytes {
		return false
	}
	c.q = append(c.q, m)
	c.qBytes += len(m.data)
	c.signal()
	return true
}

// heavy dice si la cola está cargada (para no generar parciales ni frames TTS).
func (c *Conn) heavy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.q) > heavyQueueMsgs || c.qBytes > heavyQueueBytes
}

// closeAfterFlush cierra con ese código cuando se vació la cola.
func (c *Conn) closeAfterFlush(code int, why string) {
	c.mu.Lock()
	if c.closeCode == 0 {
		c.closeCode, c.closeWhy = code, why
	}
	c.mu.Unlock()
	c.signal()
}

// closeNow cierra sin vaciar (cola llena, idle).
func (c *Conn) closeNow(code int, why string) {
	c.mu.Lock()
	c.q, c.qBytes = nil, 0
	if c.closeCode == 0 {
		c.closeCode, c.closeWhy = code, why
	}
	c.mu.Unlock()
	c.signal()
}

func (c *Conn) writer() {
	defer c.cancel()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.notify:
		}
		for {
			c.mu.Lock()
			if len(c.q) == 0 {
				code, why := c.closeCode, c.closeWhy
				c.mu.Unlock()
				if code != 0 {
					_ = c.ws.Close(websocket.StatusCode(code), why)
					return
				}
				break
			}
			m := c.q[0]
			c.q = c.q[1:]
			c.qBytes -= len(m.data)
			c.mu.Unlock()
			typ := websocket.MessageText
			if m.binary {
				typ = websocket.MessageBinary
			}
			wctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
			err := c.ws.Write(wctx, typ, m.data)
			cancel()
			if err != nil {
				_ = c.ws.CloseNow()
				return
			}
			if c.onSent != nil {
				c.onSent(m)
			}
		}
	}
}

// heartbeat manda pings de control y cierra por inactividad (4011).
func (c *Conn) heartbeat(every, idle time.Duration) {
	ping := time.NewTicker(every)
	check := time.NewTicker(time.Second)
	if idle < 5*time.Second {
		check.Reset(idle / 5)
	}
	defer ping.Stop()
	defer check.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ping.C:
			go func() {
				pctx, cancel := context.WithTimeout(c.ctx, every)
				defer cancel()
				if c.ws.Ping(pctx) == nil {
					c.touch()
				}
			}()
		case <-check.C:
			if c.idleFor() > idle {
				c.closeNow(protocol.CloseIdleTimeout, "idle timeout")
				return
			}
		}
	}
}

// envelope arma un mensaje JSON del servidor.
func envelope(typ string, seq uint32, sessionID string, ack uint32, data any) []byte {
	type env struct {
		V         int     `json:"v"`
		Type      string  `json:"type"`
		Seq       uint32  `json:"seq"`
		Ts        string  `json:"ts"`
		SessionID string  `json:"session_id"`
		Ack       *uint32 `json:"ack,omitempty"`
		Data      any     `json:"data"`
	}
	e := env{V: 1, Type: typ, Seq: seq, Ts: protocol.Now(time.Now()), SessionID: sessionID, Data: data}
	if ack > 0 {
		e.Ack = &ack
	}
	b, _ := json.Marshal(e)
	return b
}

func errData(code, msg string, fatal, retry bool, ref *uint32, closeCode int) protocol.Error {
	e := protocol.Error{Code: code, Message: msg, Fatal: fatal, Retryable: retry, RefSeq: ref}
	if closeCode != 0 {
		cc := closeCode
		e.CloseCode = &cc
	}
	return e
}

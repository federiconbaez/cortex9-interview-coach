package live

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/federiconbaez/cortex9-interview-coach/backend/internal/protocol"
)

// Serve hace el upgrade y atiende la conexión. La API ya validó auth,
// Origin, existencia de la sesión, estado y subprotocolo.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, sessionID string) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.Subprotocol}, InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ws.SetReadLimit(protocol.MaxTextMessage + 1024)
	c := newConn(ws)
	h.svc.M.WSConnections.WithLabelValues("open").Inc()
	defer h.svc.M.WSConnections.WithLabelValues("open").Dec()
	go c.writer()
	go c.heartbeat(h.opt.Heartbeat, h.opt.IdleTimeout)

	var rt *Runtime
	var helloDone atomic.Bool
	ephemeral := func(code, msg string, retry bool, close int) {
		// sin runtime (antes del hello): seq 1 en esta conexión
		c.enqueue(outMsg{seq: 1, typ: protocol.TError, data: envelope(protocol.TError, 1, sessionID, 0, errData(code, msg, true, retry, nil, close)), at: time.Now()})
		c.closeAfterFlush(close, code)
	}
	helloTimer := time.AfterFunc(h.opt.HelloTimeout, func() {
		if !helloDone.Load() {
			ephemeral(protocol.ErrHelloRequired, "Mandá hello en los primeros 5 s.", true, protocol.CloseProtocolError)
		}
	})
	defer helloTimer.Stop()

	var bpSince time.Time
	for {
		typ, data, err := ws.Read(c.ctx)
		if err != nil {
			break
		}
		c.touch()
		if rt == nil {
			var env protocol.Envelope
			var hello protocol.Hello
			if typ != websocket.MessageText || json.Unmarshal(data, &env) != nil || env.Type != protocol.THello ||
				json.Unmarshal(env.Data, &hello) != nil {
				helloDone.Store(true)
				ephemeral(protocol.ErrHelloRequired, "El primer mensaje debe ser hello.", true, protocol.CloseProtocolError)
				break
			}
			if env.SessionID != sessionID {
				helloDone.Store(true)
				ephemeral(protocol.ErrInvalidMessage, "session_id no coincide con la ruta.", false, protocol.CloseProtocolError)
				break
			}
			got, err := h.runtime(r.Context(), sessionID)
			helloDone.Store(true)
			if err != nil {
				if errors.Is(err, ErrClosed) {
					ephemeral(protocol.ErrInternal, "El servidor se está apagando.", true, protocol.CloseGoingAway)
				} else {
					ephemeral(protocol.ErrSessionEnded, "La sesión ya terminó.", false, protocol.CloseSessionEnded)
				}
				break
			}
			rt = got
			hm := helloMsg{env: env, hello: hello}
			h.svc.M.WSMessages.WithLabelValues("in", protocol.THello).Inc()
			rt.post(func() { rt.attach(c, hm) })
			continue
		}
		if typ == websocket.MessageBinary {
			hdr, payload, ferr := protocol.DecodeFrame(data)
			n := c.inflight.Add(int64(len(payload)))
			if n > int64(2*h.opt.MaxInflightBytes) {
				if bpSince.IsZero() {
					bpSince = time.Now()
					h.svc.M.WSBackpressure.WithLabelValues("warn").Inc()
					rtl := rt
					rtl.post(func() {
						if rtl.conn == c {
							rtl.sendErr(protocol.ErrBackpressure, "Demasiado audio sin procesar; frená el envío.", true, nil)
						}
					})
				} else if time.Since(bpSince) > 10*time.Second {
					h.svc.M.WSBackpressure.WithLabelValues("close").Inc()
					c.closeNow(protocol.CloseBackpressure, "backpressure")
					break
				}
			} else {
				bpSince = time.Time{}
			}
			rtl := rt
			rtl.post(func() { rtl.onFrame(c, hdr, payload, ferr) })
			continue
		}
		rtl := rt
		rtl.post(func() { rtl.onText(c, data) })
	}
	if rt != nil {
		rtl := rt
		rtl.post(func() { rtl.detach(c) })
	}
	// si había un cierre propio en curso, deja que el writer vacíe lo pendiente
	c.mu.Lock()
	closing := c.closeCode != 0
	c.mu.Unlock()
	if closing {
		select {
		case <-c.ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	c.cancel()
	_ = ws.CloseNow()
}

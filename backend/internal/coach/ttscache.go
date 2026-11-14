package coach

import (
	"sync"
	"time"
)

// TTSAudio es una intervención sintetizada (para tts_delivery=url).
type TTSAudio struct {
	Audio       []byte
	ContentType string
	DurationMs  int
	Expires     time.Time
}

// TTSCache guarda en memoria el audio TTS por (sesión, utterance) con TTL y
// tope de bytes (se descarta lo más viejo).
type TTSCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	size  int
	items map[string]*TTSAudio
	order []string
	Now   func() time.Time
}

func NewTTSCache(ttl time.Duration, maxBytes int) *TTSCache {
	return &TTSCache{ttl: ttl, max: maxBytes, items: map[string]*TTSAudio{}, Now: time.Now}
}

func key(sess, utt string) string { return sess + "/" + utt }

// Put guarda y devuelve el vencimiento.
func (c *TTSCache) Put(sess, utt string, a TTSAudio) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	a.Expires = c.Now().Add(c.ttl)
	k := key(sess, utt)
	if old, ok := c.items[k]; ok {
		c.size -= len(old.Audio)
	} else {
		c.order = append(c.order, k)
	}
	c.items[k] = &a
	c.size += len(a.Audio)
	for c.size > c.max && len(c.order) > 1 {
		c.evict(c.order[0])
	}
	return a.Expires
}

func (c *TTSCache) evict(k string) {
	if it, ok := c.items[k]; ok {
		c.size -= len(it.Audio)
		delete(c.items, k)
	}
	for i, o := range c.order {
		if o == k {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

func (c *TTSCache) Get(sess, utt string) (*TTSAudio, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key(sess, utt)]
	if !ok {
		return nil, false
	}
	if c.Now().After(it.Expires) {
		c.evict(key(sess, utt))
		return nil, false
	}
	return it, true
}

// DropSession borra el audio de una sesión.
func (c *TTSCache) DropSession(sess string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range append([]string(nil), c.order...) {
		if len(k) > len(sess) && k[:len(sess)+1] == sess+"/" {
			c.evict(k)
		}
	}
}

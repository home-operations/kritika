package egress

import "time"

// recentlyRefilled reports whether a retry replaced the entry under key
// within refillCooldown, so another retry is served the entry instead.
func (c *Cache) recentlyRefilled(key string) bool {
	c.refilling.Lock()
	defer c.refilling.Unlock()
	now := time.Now()
	for k, at := range c.refills {
		if now.Sub(at) >= refillCooldown {
			delete(c.refills, k)
		}
	}
	_, recent := c.refills[key]
	return recent
}

// refilled records that a retry has just replaced the entry under key.
func (c *Cache) refilled(key string) {
	c.refilling.Lock()
	defer c.refilling.Unlock()
	if c.refills == nil {
		c.refills = map[string]time.Time{}
	}
	if len(c.refills) >= maxRefills {
		oldest, at := "", time.Time{}
		for k, t := range c.refills {
			if oldest == "" || t.Before(at) {
				oldest, at = k, t
			}
		}
		delete(c.refills, oldest)
	}
	c.refills[key] = time.Now()
}

package platform

import (
	"errors"

	"github.com/redis/go-redis/v9"
)

// OpenRedis returns a client for url without connecting to it. Redis holds
// only queued tasks and rate-limit counters, so the api starts while Redis is
// down and /readyz reports redis: fail, where a missing PostgreSQL stops
// start-up. Errors never quote the URL, which may carry a password and is
// logged.
func OpenRedis(url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, errors.New("open redis: the connection URL is invalid")
	}
	return redis.NewClient(opts), nil
}

package promapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/klauspost/compress/gzhttp"
	"github.com/rs/zerolog/log"
	"go.uber.org/ratelimit"

	"github.com/cloudflare/pint/internal/output"
)

type QueryError struct {
	err error
	msg string
}

func (qe QueryError) Error() string {
	return qe.msg
}

func (qe QueryError) Unwrap() error {
	return qe.err
}

type querier interface {
	Endpoint() string
	String() string
	CacheKey() uint64
	CacheTTL() time.Duration
	Run() queryResult
}

type queryRequest struct {
	query  querier
	result chan queryResult
}

type queryResult struct {
	value any
	err   error
}

func sanitizeURI(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	if u.User != nil {
		if _, pwdSet := u.User.Password(); pwdSet {
			u.User = url.UserPassword(u.User.Username(), "xxx")
		}
		return u.String()
	}
	return s
}

type Prometheus struct {
	name        string
	unsafeURI   string
	safeURI     string
	headers     map[string]string
	timeout     time.Duration
	concurrency int
	client      http.Client
	cache       *queryCache
	locker      *partitionLocker
	rateLimiter ratelimit.Limiter
	wg          sync.WaitGroup
	queries     chan queryRequest
}

func NewPrometheus(name, uri string, headers map[string]string, timeout time.Duration, concurrency, rl int) *Prometheus {
	prom := Prometheus{
		name:        name,
		unsafeURI:   uri,
		safeURI:     sanitizeURI(uri),
		headers:     headers,
		timeout:     timeout,
		client:      http.Client{Transport: gzhttp.Transport(http.DefaultTransport)},
		locker:      newPartitionLocker((&sync.Mutex{})),
		rateLimiter: ratelimit.New(rl),
		concurrency: concurrency,
	}
	return &prom
}

func (prom *Prometheus) Close() {
	log.Debug().Str("name", prom.name).Str("uri", prom.safeURI).Msg("Stopping query workers")
	close(prom.queries)
	prom.wg.Wait()
}

func (prom *Prometheus) StartWorkers() {
	log.Debug().
		Str("name", prom.name).
		Str("uri", prom.safeURI).
		Int("workers", prom.concurrency).
		Msg("Starting query workers")

	prom.queries = make(chan queryRequest, prom.concurrency*10)

	for w := 1; w <= prom.concurrency; w++ {
		prom.wg.Add(1)
		go func() {
			defer prom.wg.Done()
			queryWorker(prom, prom.queries)
		}()
	}
}

func (prom *Prometheus) doRequest(ctx context.Context, method, path string, args url.Values) (*http.Response, error) {
	u, _ := url.Parse(prom.unsafeURI)
	u.Path = strings.TrimSuffix(u.Path, "/")

	uri, err := url.JoinPath(u.String(), path)
	if err != nil {
		return nil, err
	}

	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(args.Encode())
	} else if eargs := args.Encode(); eargs != "" {
		uri += "?" + eargs
	}

	req, err := http.NewRequestWithContext(ctx, method, uri, body)
	if err != nil {
		return nil, err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	for k, v := range prom.headers {
		req.Header.Set(k, v)
	}

	return prom.client.Do(req)
}

func queryWorker(prom *Prometheus, queries chan queryRequest) {
	for job := range queries {
		job.result <- processJob(prom, job)
	}
}

func processJob(prom *Prometheus, job queryRequest) queryResult {
	cacheKey := job.query.CacheKey()
	if prom.cache != nil {
		if cached, ok := prom.cache.get(cacheKey, job.query.Endpoint()); ok {
			log.Debug().
				Str("uri", prom.safeURI).
				Str("query", job.query.String()).
				Uint64("key", cacheKey).
				Msg("Cache hit")
			return queryResult{value: cached}
		}
	}

	log.Debug().
		Str("uri", prom.safeURI).
		Str("query", job.query.String()).
		Uint64("key", cacheKey).
		Msg("Cache miss")

	prometheusQueriesTotal.WithLabelValues(prom.name, job.query.Endpoint()).Inc()
	prometheusQueriesRunning.WithLabelValues(prom.name, job.query.Endpoint()).Inc()

	prom.rateLimiter.Take()
	start := time.Now()
	result := job.query.Run()
	dur := time.Since(start)
	log.Debug().
		Str("uri", prom.safeURI).
		Str("query", job.query.String()).
		Str("endpoint", job.query.Endpoint()).
		Str("duration", output.HumanizeDuration(dur)).
		Msg("Query completed")
	prometheusQueriesRunning.WithLabelValues(prom.name, job.query.Endpoint()).Dec()

	if result.err != nil {
		if errors.Is(result.err, context.Canceled) {
			return result
		}
		prometheusQueryErrorsTotal.WithLabelValues(prom.name, job.query.Endpoint(), errReason(result.err)).Inc()
		log.Error().
			Err(result.err).
			Str("uri", prom.safeURI).
			Str("query", job.query.String()).
			Msg("Query returned an error")
		return result
	}

	if prom.cache != nil {
		prom.cache.set(cacheKey, result.value, job.query.CacheTTL(), job.query.Endpoint())
	}

	return result
}

func formatTime(t time.Time) string {
	return strconv.FormatFloat(float64(t.Unix())+float64(t.Nanosecond())/1e9, 'f', -1, 64)
}

func dummyReadAll(r io.Reader) {
	_, _ = io.Copy(io.Discard, r)
}

func hash(s ...string) uint64 {
	h := xxhash.New()
	for _, v := range s {
		_, _ = h.WriteString(v)
		_, _ = h.WriteString("\n")
	}
	return h.Sum64()
}

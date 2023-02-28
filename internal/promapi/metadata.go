package promapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prymitive/current"
	"github.com/rs/zerolog/log"
)

type MetadataResult struct {
	URI      string
	Metadata []v1.Metadata
}

type metadataQuery struct {
	prom      *Prometheus
	ctx       context.Context
	metric    string
	timestamp time.Time
}

func (q metadataQuery) Run() queryResult {
	log.Debug().
		Str("uri", q.prom.safeURI).
		Str("metric", q.metric).
		Msg("Getting prometheus metrics metadata")

	ctx, cancel := context.WithTimeout(q.ctx, q.prom.timeout)
	defer cancel()

	var qr queryResult

	args := url.Values{}
	args.Set("metric", q.metric)
	resp, err := q.prom.doRequest(ctx, http.MethodGet, q.Endpoint(), args)
	if err != nil {
		qr.err = fmt.Errorf("failed to query Prometheus metrics metadata: %w", err)
		return qr
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		qr.err = tryDecodingAPIError(resp)
		return qr
	}

	meta, err := streamMetadata(resp.Body)
	qr.value, qr.err = meta, err
	return qr
}

func (q metadataQuery) Endpoint() string {
	return "/api/v1/metadata"
}

func (q metadataQuery) String() string {
	return q.metric
}

func (q metadataQuery) CacheKey() uint64 {
	return hash(q.prom.unsafeURI, q.Endpoint(), q.metric)
}

func (q metadataQuery) CacheTTL() time.Duration {
	return time.Minute * 10
}

func (p *Prometheus) Metadata(ctx context.Context, metric string) (*MetadataResult, error) {
	log.Debug().Str("uri", p.safeURI).Str("metric", metric).Msg("Scheduling Prometheus metrics metadata query")

	key := fmt.Sprintf("/api/v1/metadata/%s", metric)
	p.locker.lock(key)
	defer p.locker.unlock(key)

	resultChan := make(chan queryResult)
	p.queries <- queryRequest{
		query:  metadataQuery{prom: p, ctx: ctx, metric: metric, timestamp: time.Now()},
		result: resultChan,
	}

	result := <-resultChan
	if result.err != nil {
		return nil, QueryError{err: result.err, msg: decodeError(result.err)}
	}

	metadata := MetadataResult{URI: p.safeURI, Metadata: result.value.(map[string][]v1.Metadata)[metric]}

	return &metadata, nil
}

func streamMetadata(r io.Reader) (meta map[string][]v1.Metadata, err error) {
	defer dummyReadAll(r)

	var status, errType, errText string
	meta = map[string][]v1.Metadata{}
	decoder := current.Object(
		current.Key("status", current.Value(func(s string, isNil bool) {
			status = s
		})),
		current.Key("error", current.Value(func(s string, isNil bool) {
			errText = s
		})),
		current.Key("errorType", current.Value(func(s string, isNil bool) {
			errType = s
		})),
		current.Key("data", current.Map(func(k string, v []v1.Metadata) {
			meta[k] = v
		})),
	)

	dec := json.NewDecoder(r)
	if err = decoder.Stream(dec); err != nil {
		return nil, APIError{Status: status, ErrorType: v1.ErrBadResponse, Err: fmt.Sprintf("JSON parse error: %s", err)}
	}

	if status != "success" {
		return nil, APIError{Status: status, ErrorType: decodeErrorType(errType), Err: errText}
	}

	return meta, nil
}

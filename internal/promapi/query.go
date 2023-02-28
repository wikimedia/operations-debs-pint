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
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prymitive/current"
	"github.com/rs/zerolog/log"
)

type QueryResult struct {
	URI    string
	Series []Sample
}

type instantQuery struct {
	prom      *Prometheus
	ctx       context.Context
	expr      string
	timestamp time.Time
}

func (q instantQuery) Run() queryResult {
	log.Debug().
		Str("uri", q.prom.safeURI).
		Str("query", q.expr).
		Msg("Running prometheus query")

	ctx, cancel := context.WithTimeout(q.ctx, q.prom.timeout)
	defer cancel()

	var qr queryResult

	args := url.Values{}
	args.Set("query", q.expr)
	args.Set("timeout", q.prom.timeout.String())
	resp, err := q.prom.doRequest(ctx, http.MethodPost, q.Endpoint(), args)
	if err != nil {
		qr.err = err
		return qr
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		qr.err = tryDecodingAPIError(resp)
		return qr
	}

	samples, err := streamSamples(resp.Body)
	qr.value, qr.err = samples, err
	return qr
}

func (q instantQuery) Endpoint() string {
	return "/api/v1/query"
}

func (q instantQuery) String() string {
	return q.expr
}

func (q instantQuery) CacheKey() uint64 {
	return hash(q.prom.unsafeURI, q.Endpoint(), q.expr)
}

func (q instantQuery) CacheTTL() time.Duration {
	return time.Minute * 5
}

func (p *Prometheus) Query(ctx context.Context, expr string) (*QueryResult, error) {
	log.Debug().Str("uri", p.safeURI).Str("query", expr).Msg("Scheduling prometheus query")

	key := fmt.Sprintf("/api/v1/query/%s", expr)
	p.locker.lock(key)
	defer p.locker.unlock(key)

	resultChan := make(chan queryResult)
	p.queries <- queryRequest{
		query:  instantQuery{prom: p, ctx: ctx, expr: expr, timestamp: time.Now()},
		result: resultChan,
	}

	result := <-resultChan
	if result.err != nil {
		return nil, QueryError{err: result.err, msg: decodeError(result.err)}
	}

	qr := QueryResult{
		URI:    p.safeURI,
		Series: result.value.([]Sample),
	}
	log.Debug().Str("uri", p.safeURI).Str("query", expr).Int("series", len(qr.Series)).Msg("Parsed response")

	return &qr, nil
}

type Sample struct {
	Labels labels.Labels
	Value  float64
}

func streamSamples(r io.Reader) (samples []Sample, err error) {
	defer dummyReadAll(r)

	var status, resultType, errType, errText string
	samples = []Sample{}
	var sample model.Sample
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
		current.Key("data", current.Object(
			current.Key("resultType", current.Value(func(s string, isNil bool) {
				resultType = s
			})),
			current.Key("result", current.Array(
				&sample,
				func() {
					samples = append(samples, Sample{
						Labels: MetricToLabels(sample.Metric),
						Value:  float64(sample.Value),
					})
					sample.Metric = model.Metric{}
				},
			)),
		)),
	)

	dec := json.NewDecoder(r)
	if err = decoder.Stream(dec); err != nil {
		return nil, APIError{Status: status, ErrorType: v1.ErrBadResponse, Err: fmt.Sprintf("JSON parse error: %s", err)}
	}

	if status != "success" {
		return nil, APIError{Status: status, ErrorType: decodeErrorType(errType), Err: errText}
	}

	if resultType != "vector" {
		return nil, APIError{Status: status, ErrorType: v1.ErrBadResponse, Err: fmt.Sprintf("invalid result type, expected vector, got %s", resultType)}
	}

	return samples, nil
}

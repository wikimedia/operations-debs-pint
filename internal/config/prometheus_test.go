package config

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrometheusConfig(t *testing.T) {
	type testCaseT struct {
		conf PrometheusConfig
		err  error
	}

	testCases := []testCaseT{
		{
			conf: PrometheusConfig{
				Name:    "prom",
				URI:     "http://localhost",
				Timeout: "5m",
				Include: []string{"foo", "bar"},
			},
		},
		{
			conf: PrometheusConfig{
				Name:     "prom",
				URI:      "http://localhost",
				Failover: []string{"http://localhost", "http://localhost"},
				Timeout:  "5m",
				Include:  []string{"foo", "bar"},
			},
		},
		{
			conf: PrometheusConfig{URI: "http://localhost"},
		},
		{
			conf: PrometheusConfig{},
			err:  errors.New("prometheus URI cannot be empty"),
		},
		{
			conf: PrometheusConfig{
				URI:     "http://localhost",
				Timeout: "foo",
			},
			err: errors.New(`not a valid duration string: "foo"`),
		},
		{
			conf: PrometheusConfig{
				Name:    "prom",
				URI:     "http://localhost",
				Timeout: "5m",
				Include: []string{"foo.++", "bar"},
			},
			err: errors.New("error parsing regexp: invalid nested repetition operator: `++`"),
		},
		{
			conf: PrometheusConfig{
				Name:    "prom",
				URI:     "http://localhost",
				Timeout: "5m",
				Exclude: []string{"foo.++", "bar"},
			},
			err: errors.New("error parsing regexp: invalid nested repetition operator: `++`"),
		},
		{
			conf: PrometheusConfig{
				Name:    "prom",
				URI:     "http://localhost",
				Timeout: "5m",
				Uptime:  "xxx{foo=bar}",
			},
			err: errors.New(`invalid Prometheus uptime metric selector "xxx{foo=bar}": 1:8: expected '==', found '='`),
		},
		{
			conf: PrometheusConfig{
				Name: "prom",
				URI:  "http://localhost",
				Tags: []string{"a b c"},
			},
			err: errors.New(`prometheus tag "a b c" cannot contain " "`),
		},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%v", tc.conf), func(t *testing.T) {
			err := tc.conf.validate()
			if err == nil || tc.err == nil {
				require.Equal(t, err, tc.err)
			} else {
				require.EqualError(t, err, tc.err.Error())
			}
		})
	}
}

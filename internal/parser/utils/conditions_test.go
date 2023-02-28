package utils_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudflare/pint/internal/parser/utils"
)

func TestRemoveConditions(t *testing.T) {
	type testCaseT struct {
		input  string
		output string
	}

	testCases := []testCaseT{
		{
			input:  "100",
			output: "100",
		},
		{
			input:  "(100)",
			output: "100",
		},
		{
			input:  "100 ^ 2",
			output: "",
		},
		{
			input:  "(1024 ^ 2)",
			output: "",
		},
		{
			input:  "(100*(1024^2))",
			output: "",
		},
		{
			input:  "min_over_time((foo_with_notfound > 0)[30m:1m]) / bar",
			output: "min_over_time(foo_with_notfound[30m:1m]) / bar",
		},
		{
			input:  "min_over_time((foo_with_notfound > 0)[30m:1m]) / 2",
			output: "min_over_time(foo_with_notfound[30m:1m])",
		},
		{
			input:  "min_over_time(rate(http_requests_total[5m])[30m:1m])",
			output: "min_over_time(rate(http_requests_total[5m])[30m:1m])",
		},
		{
			input:  "(memory_bytes / ignoring(job) (memory_limit > 0)) * on(app_name) group_left(a,b,c) app_registry",
			output: "(memory_bytes / ignoring (job) memory_limit) * on (app_name) group_left (a, b, c) app_registry",
		},
		{
			input:  `(quantile_over_time(0.9, (rate(container_cpu_system_seconds_total{app_name="foo"}[5m]) + rate(container_cpu_user_seconds_total{app_name="foo"}[5m]))[5m:]) / on(instance) bar) > 0.65`,
			output: `(quantile_over_time(0.9, (rate(container_cpu_system_seconds_total{app_name="foo"}[5m]) + rate(container_cpu_user_seconds_total{app_name="foo"}[5m]))[5m:]) / on (instance) bar)`,
		},
		{
			input:  "sum(foo > 5)",
			output: "sum(foo)",
		},
		{
			input:  `predict_linear(ceph_pool_max_avail[2d], 3600 * 24 * 5) * on(pool_id) group_left(name) ceph_pool_metadata < 0`,
			output: `predict_linear(ceph_pool_max_avail[2d], 3600 * 24 * 5) * on (pool_id) group_left (name) ceph_pool_metadata`,
		},
		{
			input:  `label_join(sum by (job, cluster, id) (rate(errors_total[5m])), "_tmp", "/", "cluster", "id")`,
			output: `label_join(sum by (job, cluster, id) (rate(errors_total[5m])), "_tmp", "/", "cluster", "id")`,
		},
		{
			input:  `round((foo > 0))`,
			output: `round(foo)`,
		},
		{
			input:  `round((foo > 0), 10)`,
			output: `round(foo, 10)`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			output := utils.RemoveConditions(tc.input)
			require.Equalf(t, tc.output, output.String(), "input: %q", tc.input)
		})
	}
}

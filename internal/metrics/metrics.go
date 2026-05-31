/*
Copyright 2026 The huawei-sfs-operator Authors.
Licensed under the Apache License, Version 2.0.
*/

// Package metrics exposes the operator's Prometheus counters and
// histograms. Registers into controller-runtime's metrics registry at
// package init() so kube-prometheus-stack picks them up automatically
// via the operator's Service + ServiceMonitor (planned for a future release).
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// ReconcileTotal counts reconcile outcomes per CR. The `result` label
// is one of "success" / "create_failed" / "delete_failed" /
// "api_error" / "requeued" so a single PromQL like
// `rate(huawei_sfs_operator_reconcile_total{result!="success"}[5m])`
// catches every failure mode.
var ReconcileTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "huawei_sfs_operator_reconcile_total",
		Help: "SfsTurboInstance reconcile attempts, labelled by outcome.",
	},
	[]string{"namespace", "result"},
)

// HuaweiAPISeconds measures latency of every HuaweiCloud API call the
// operator makes. The `op` label is "create" / "get" / "delete".
// Buckets pick up p50/p95/p99 around the SFS Turbo API's typical
// shape (10ms-30s).
var HuaweiAPISeconds = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name: "huawei_sfs_operator_huawei_api_seconds",
		Help: "Wall-clock duration of HuaweiCloud SFS Turbo API calls.",
		Buckets: []float64{
			0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30,
		},
	},
	[]string{"op", "result"},
)

// FsLifecyclePhase reports the current phase per CR — gauges so we can
// alert on "stuck Provisioning > 10 min" or "Failed > 5 min". The
// `phase` label is one of the controller's condition types.
var FsLifecyclePhase = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "huawei_sfs_operator_fs_phase",
		Help: "Current condition phase for each SfsTurboInstance (1 = in this phase, 0 = not).",
	},
	[]string{"namespace", "name", "phase"},
)

func init() {
	metrics.Registry.MustRegister(
		ReconcileTotal,
		HuaweiAPISeconds,
		FsLifecyclePhase,
	)
}

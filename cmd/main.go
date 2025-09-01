/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"crypto/tls"
	"flag"
	"os"
	"path/filepath"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"github.com/vitistack/kubevirt-operator/internal/services/initializationservice"
	"github.com/vitistack/kubevirt-operator/internal/settings"
	"github.com/vitistack/kubevirt-operator/pkg/clients"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	netattdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	vitistackv1alpha1 "github.com/vitistack/crds/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/controllers"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	// +kubebuilder:scaffold:imports
)

// Configuration holds all the runtime configuration parameters
type Configuration struct {
	MetricsAddr          string
	MetricsCertPath      string
	MetricsCertName      string
	MetricsCertKey       string
	WebhookCertPath      string
	WebhookCertName      string
	WebhookCertKey       string
	EnableLeaderElection bool
	ProbeAddr            string
	SecureMetrics        bool
	EnableHTTP2          bool
}

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(vitistackv1alpha1.AddToScheme(scheme))
	utilruntime.Must(kubevirtv1.AddToScheme(scheme))
	// Register Multus NetworkAttachmentDefinition CRD types so we can list them
	utilruntime.Must(netattdefv1.AddToScheme(scheme))

	// +kubebuilder:scaffold:scheme
}

func main() {
	settings.Init()
	// Parse command line flags
	config := parseFlags()

	// Initialize clients and check prerequisites
	clients.Init()
	initializationservice.CheckPrerequisites()

	// Set up TLS options
	tlsOpts := setupTLSOptions(config.EnableHTTP2)

	// Set up webhook server
	webhookServer, webhookCertWatcher := setupWebhookServer(config, tlsOpts)

	// Set up metrics server
	metricsOpts, metricsCertWatcher := setupMetricsServer(config, tlsOpts)

	// Create and configure the manager
	mgr := setupManager(config, &metricsOpts, webhookServer)

	// Add certificate watchers to the manager
	addWatchersToManager(mgr, metricsCertWatcher, webhookCertWatcher)

	// Set up health and readiness checks
	setupHealthChecks(mgr)

	// Start the manager
	startManager(mgr)
}

// parseFlags parses command line flags and returns a Configuration object
func parseFlags() *Configuration {
	config := &Configuration{}

	flag.StringVar(&config.MetricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&config.ProbeAddr, "health-probe-bind-address", ":9992", "The address the probe endpoint binds to.")
	flag.BoolVar(&config.EnableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&config.SecureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&config.WebhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&config.WebhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&config.WebhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&config.MetricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&config.MetricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&config.MetricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&config.EnableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")

	// Configure zap logger
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	// Set up the logger
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	return config
}

// setupTLSOptions configures TLS options based on HTTP2 settings
func setupTLSOptions(enableHTTP2 bool) []func(*tls.Config) {
	var tlsOpts []func(*tls.Config)

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	if !enableHTTP2 {
		disableHTTP2 := func(c *tls.Config) {
			setupLog.Info("disabling http/2")
			c.NextProtos = []string{"http/1.1"}
		}
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	return tlsOpts
}

// setupWebhookServer configures and returns the webhook server and its certificate watcher
func setupWebhookServer(config *Configuration, tlsOpts []func(*tls.Config)) (webhook.Server, *certwatcher.CertWatcher) {
	// Initial webhook TLS options
	webhookTLSOpts := make([]func(*tls.Config), len(tlsOpts))
	copy(webhookTLSOpts, tlsOpts)

	var webhookCertWatcher *certwatcher.CertWatcher

	if len(config.WebhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", config.WebhookCertPath,
			"webhook-cert-name", config.WebhookCertName,
			"webhook-cert-key", config.WebhookCertKey)

		var err error
		webhookCertWatcher, err = certwatcher.New(
			filepath.Join(config.WebhookCertPath, config.WebhookCertName),
			filepath.Join(config.WebhookCertPath, config.WebhookCertKey),
		)
		if err != nil {
			setupLog.Error(err, "Failed to initialize webhook certificate watcher")
			os.Exit(1)
		}

		webhookTLSOpts = append(webhookTLSOpts, func(tlsConfig *tls.Config) {
			tlsConfig.GetCertificate = webhookCertWatcher.GetCertificate
		})
	}
	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: webhookTLSOpts,
	})

	return webhookServer, webhookCertWatcher
}

// setupMetricsServer configures the metrics server options and returns the metrics certificate watcher
func setupMetricsServer(config *Configuration, tlsOpts []func(*tls.Config)) (metricsserver.Options, *certwatcher.CertWatcher) {
	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.4/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   config.MetricsAddr,
		SecureServing: config.SecureMetrics,
		TLSOpts:       tlsOpts,
	}

	if config.SecureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.4/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	var metricsCertWatcher *certwatcher.CertWatcher

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(config.MetricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", config.MetricsCertPath,
			"metrics-cert-name", config.MetricsCertName,
			"metrics-cert-key", config.MetricsCertKey)

		var err error
		metricsCertWatcher, err = certwatcher.New(
			filepath.Join(config.MetricsCertPath, config.MetricsCertName),
			filepath.Join(config.MetricsCertPath, config.MetricsCertKey),
		)
		if err != nil {
			setupLog.Error(err, "to initialize metrics certificate watcher", "error", err)
			os.Exit(1)
		}

		metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, func(tlsConfig *tls.Config) {
			tlsConfig.GetCertificate = metricsCertWatcher.GetCertificate
		})
	}

	return metricsServerOptions, metricsCertWatcher
}

// setupManager creates and configures the controller manager
func setupManager(config *Configuration, metricsOpts *metricsserver.Options, webhookServer webhook.Server) ctrl.Manager {
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                *metricsOpts,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: config.ProbeAddr,
		LeaderElection:         config.EnableLeaderElection,
		LeaderElectionID:       "09393f52.vitistack.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	return mgr
}

// addWatchersToManager adds the certificate watchers to the manager
func addWatchersToManager(mgr ctrl.Manager, metricsCertWatcher, webhookCertWatcher *certwatcher.CertWatcher) {
	// Setup controllers
	machineReconciler := controllers.NewMachineReconciler(mgr.GetClient(), mgr.GetScheme())
	if err := machineReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Machine")
		os.Exit(1)
	}

	// +kubebuilder:scaffold:builder

	if metricsCertWatcher != nil {
		setupLog.Info("Adding metrics certificate watcher to manager")
		if err := mgr.Add(metricsCertWatcher); err != nil {
			setupLog.Error(err, "unable to add metrics certificate watcher to manager")
			os.Exit(1)
		}
	}

	if webhookCertWatcher != nil {
		setupLog.Info("Adding webhook certificate watcher to manager")
		if err := mgr.Add(webhookCertWatcher); err != nil {
			setupLog.Error(err, "unable to add webhook certificate watcher to manager")
			os.Exit(1)
		}
	}
}

// setupHealthChecks configures the health and readiness checks for the manager
func setupHealthChecks(mgr ctrl.Manager) {
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}
}

// startManager starts the controller manager
func startManager(mgr ctrl.Manager) {
	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

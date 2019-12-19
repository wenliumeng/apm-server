// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package beater

import (
	"context"
	"net"
	"net/http"

	"go.elastic.co/apm"
	"go.elastic.co/apm/module/apmhttp"
	"golang.org/x/net/netutil"

	"github.com/elastic/beats/libbeat/common/transport/tlscommon"
	"github.com/elastic/beats/libbeat/logp"

	"github.com/elastic/apm-server/beater/api"
	"github.com/elastic/apm-server/beater/config"
	"github.com/elastic/apm-server/publish"
)

type httpServer struct {
	*http.Server
	cfg    *config.Config
	logger *logp.Logger
}

func newHTTPServer(logger *logp.Logger, cfg *config.Config, tracer *apm.Tracer, reporter publish.Reporter) (*httpServer, error) {
	mux, err := api.NewMux(cfg, reporter)
	if err != nil {
		return nil, err
	}

	server := &http.Server{
		Addr: cfg.Host,
		Handler: apmhttp.Wrap(mux,
			apmhttp.WithServerRequestIgnorer(doNotTrace),
			apmhttp.WithTracer(tracer),
		),
		IdleTimeout:    cfg.IdleTimeout,
		ReadTimeout:    cfg.ReadTimeout,
		WriteTimeout:   cfg.WriteTimeout,
		MaxHeaderBytes: cfg.MaxHeaderSize,
	}

	if cfg.TLS.IsEnabled() {
		tlsServerConfig, err := tlscommon.LoadTLSServerConfig(cfg.TLS)
		if err != nil {
			return nil, err
		}
		server.TLSConfig = tlsServerConfig.BuildModuleConfig(cfg.Host)
	}
	return &httpServer{server, cfg, logger}, nil
}

func (h *httpServer) start(lis net.Listener) error {
	h.logger.Infof("Listening on: %s", h.Server.Addr)
	switch h.cfg.RumConfig.IsEnabled() {
	case true:
		h.logger.Info("RUM endpoints enabled!")
		for _, s := range h.cfg.RumConfig.AllowOrigins {
			if s == "*" {
				h.logger.Warn("CORS related setting `apm-server.rum.allow_origins` allows all origins. Consider more restrictive setting for production use.")
				break
			}
		}
	case false:
		h.logger.Info("RUM endpoints disabled.")
	}

	if h.cfg.MaxConnections > 0 {
		lis = netutil.LimitListener(lis, h.cfg.MaxConnections)
		h.logger.Infof("Connection limit set to: %d", h.cfg.MaxConnections)
	}

	if h.TLSConfig != nil {
		h.logger.Info("SSL enabled.")
		return h.ServeTLS(lis, "", "")
	}
	if h.cfg.SecretToken != "" {
		h.logger.Warn("Secret token is set, but SSL is not enabled.")
	}
	h.logger.Info("SSL disabled.")
	return h.Serve(lis)
}

func (h *httpServer) stop() {
	h.logger.Infof("Stop listening on: %s", h.Server.Addr)
	if err := h.Shutdown(context.Background()); err != nil {
		h.logger.Errorf("error stopping http server: %s", err.Error())
		if err := h.Close(); err != nil {
			h.logger.Errorf("error closing http server: %s", err.Error())
		}
	}
}

func doNotTrace(req *http.Request) bool {
	if req.RemoteAddr == "pipe" {
		// Don't trace requests coming from self,
		// or we will go into a continuous cycle.
		return true
	}
	if req.URL.Path == api.RootPath {
		// Don't trace root url (healthcheck) requests.
		return true
	}
	return false
}

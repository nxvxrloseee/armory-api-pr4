// Command armory_api is the ПР4 backend for armory_web: a REST API over
// PostgreSQL implementing the same list/CRUD/soft-delete contract the
// client already speaks (see ~/Downloads/api/КОНТРАКТ-API.md), for the
// weapons-store domain instead of the course's library example.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/cors"

	"armory_api/internal/apperr"
	"armory_api/internal/categories"
	"armory_api/internal/clients"
	"armory_api/internal/dbx"
	"armory_api/internal/designers"
	"armory_api/internal/manufacturers"
	"armory_api/internal/openapi"
	"armory_api/internal/weapons"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	dsn := env("DATABASE_URL", "postgres://armory:armory@localhost:5432/armory")
	port := env("PORT", "8080")
	origin := env("ALLOWED_ORIGIN", "http://localhost:5555")

	ctx := context.Background()
	pool, err := dbx.Connect(ctx, dsn)
	if err != nil {
		log.Fatalf("не удалось подключиться к PostgreSQL: %v", err)
	}
	defer pool.Close()

	if err := dbx.ApplySchema(ctx, pool); err != nil {
		log.Fatalf("не удалось применить схему: %v", err)
	}
	log.Println("схема применена, стартовые данные загружены (если их ещё не было)")

	r := chi.NewRouter()
	r.Use(requestLog)

	// §6.1 контракта: точные заголовки CORS на каждый ответ + обработчик
	// OPTIONS на всех адресах. rs/cors отвечает на preflight сам, не
	// доходя до testHooks ниже — как и в учебном mock-сервере, задержка и
	// принудительная ошибка не должны цеплять сам preflight.
	corsMW := cors.New(cors.Options{
		AllowedOrigins:   []string{origin},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization"},
		AllowCredentials: true,
		MaxAge:           86400,
	})
	r.Use(corsMW.Handler)
	r.Use(testHooks)

	r.Get("/api/__health", func(w http.ResponseWriter, req *http.Request) {
		apperr.WriteJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"time":   time.Now().UTC().Format(time.RFC3339),
		})
	})
	r.Post("/api/__reset", func(w http.ResponseWriter, req *http.Request) {
		if err := dbx.Reset(req.Context(), pool); err != nil {
			apperr.Write(w, err)
			return
		}
		apperr.WriteJSON(w, http.StatusOK, map[string]string{"message": "Данные восстановлены в исходное состояние"})
	})

	r.Mount("/api/weapons", weapons.Routes(pool))
	r.Mount("/api/manufacturers", manufacturers.Routes(pool))
	r.Mount("/api/categories", categories.Routes(pool))
	r.Mount("/api/designers", designers.Routes(pool))
	r.Mount("/api/clients", clients.Routes(pool))

	r.Get("/api/openapi.yaml", openapi.SpecHandler)
	r.Get("/docs", openapi.DocsHandler)

	srv := &http.Server{Addr: ":" + port, Handler: r}

	go func() {
		log.Printf("armory_api слушает http://localhost:%s/api (CORS: %s)", port, origin)
		log.Printf("документация (Swagger UI): http://localhost:%s/docs", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("сервер остановился с ошибкой: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
	log.Println("завершение работы...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		started := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, req)
		log.Printf("%-6s %s%s -> %d (%s)", req.Method, req.URL.Path, queryOrEmpty(req), sw.status, time.Since(started))
	})
}

func queryOrEmpty(req *http.Request) string {
	if req.URL.RawQuery == "" {
		return ""
	}
	return "?" + req.URL.RawQuery
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// testHooks reproduces the two diagnostic switches mock-server.js документed
// in its header comment and that the assignment's grading steps rely on
// directly: ?__fail=<status> forces that status on any request, and
// ?__delay=<ms> (capped at 10s) delays it — used to demonstrate the error
// state and the loading indicator without needing to actually stop the
// server or throttle the network.
func testHooks(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		if raw := q.Get("__delay"); raw != "" {
			if ms, err := strconv.Atoi(raw); err == nil && ms > 0 {
				if ms > 10000 {
					ms = 10000
				}
				time.Sleep(time.Duration(ms) * time.Millisecond)
			}
		}
		if raw := q.Get("__fail"); raw != "" {
			if status, err := strconv.Atoi(raw); err == nil && status >= 400 && status < 600 {
				apperr.WriteJSON(w, status, map[string]string{"message": "Ошибка вызвана намеренно параметром __fail"})
				return
			}
		}
		next.ServeHTTP(w, req)
	})
}

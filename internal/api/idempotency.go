package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"

	"gepa-cli/internal/store"
)

// idempotencyMiddleware hace que cualquier POST o DELETE que traiga la
// cabecera Idempotency-Key se ejecute UNA sola vez, aunque llegue repetido
// (doble click, reintento del cliente, reintento de un proxy) y aunque cada
// copia caiga en una réplica distinta detrás de Nginx.
//
//  1. Primera vez: se reserva la key en Redis (SET NX), se procesa la
//     request y, si salió bien (2xx), se guarda la respuesta.
//  2. Repetida: no se procesa de nuevo; se devuelve la respuesta guardada
//     con la cabecera Idempotent-Replayed: true.
//  3. Repetida mientras la primera sigue en curso: 409, reintentá después.
//  4. Misma key con otro body o en otra ruta: 422, es un error del cliente.
//
// Si la request falla (4xx/5xx) la key se libera: no hubo efectos, así que
// reintentar con la misma key es seguro.
func idempotencyMiddleware(s *store.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" || (r.Method != http.MethodPost && r.Method != http.MethodDelete) {
			next.ServeHTTP(w, r)
			return
		}
		if len(key) > 128 {
			writeError(w, http.StatusBadRequest, "Idempotency-Key demasiado larga (máx. 128)")
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeError(w, http.StatusBadRequest, "no se pudo leer el body")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		// La "huella" de la request: misma key + misma huella = mismo intento.
		sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"), body...))
		fingerprint := hex.EncodeToString(sum[:])

		rec, err := s.IdemBegin(r.Context(), key, fingerprint)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if rec != nil {
			switch {
			case rec.Fingerprint != fingerprint:
				writeError(w, http.StatusUnprocessableEntity, "la Idempotency-Key ya se usó con otra solicitud")
			case rec.Status == 0:
				writeError(w, http.StatusConflict, "una solicitud con esta Idempotency-Key sigue en curso")
			default:
				w.Header().Set("Idempotent-Replayed", "true")
				if len(rec.Body) > 0 {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(rec.Status)
				_, _ = w.Write(rec.Body)
			}
			return
		}

		rw := &recordingWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		// context.WithoutCancel: si el cliente cortó la conexión, igual hay
		// que guardar el resultado; justamente ese cliente va a reintentar.
		ctx := context.WithoutCancel(r.Context())
		if rw.status >= 200 && rw.status < 300 {
			_ = s.IdemSave(ctx, key, store.IdemRecord{Fingerprint: fingerprint, Status: rw.status, Body: rw.body.Bytes()})
		} else {
			_ = s.IdemRelease(ctx, key)
		}
	})
}

// recordingWriter deja pasar la respuesta al cliente y además se queda con
// una copia para poder guardarla.
type recordingWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *recordingWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *recordingWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

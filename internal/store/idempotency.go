package store

// Registro de idempotency keys. Vive en Redis y no en la memoria de cada
// réplica: detrás de Nginx el reintento de una request puede caer en otra
// instancia, y esa tiene que saber que la primera ya se procesó.
//
// Esquema: "gepa:idem:{key}" -> IdemRecord en JSON, con vencimiento.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// idemTTL: cuánto se recuerda una respuesta ya enviada.
	idemTTL = 24 * time.Hour
	// idemPendingTTL: si la réplica se cae a mitad de la request, la
	// reserva se libera sola y el cliente puede reintentar.
	idemPendingTTL = 30 * time.Second
)

// IdemRecord es lo que se guarda por cada idempotency key. Status 0 significa
// "en curso": la primera request todavía no terminó.
type IdemRecord struct {
	Fingerprint string `json:"fingerprint"`
	Status      int    `json:"status"`
	Body        []byte `json:"body,omitempty"`
}

func idemKey(key string) string { return "gepa:idem:" + key }

// IdemBegin intenta reservar key. Si la reserva es nueva devuelve nil: el
// llamador debe procesar la request. Si la key ya existía devuelve lo que
// hay guardado (en curso o la respuesta ya enviada).
func (s *Store) IdemBegin(ctx context.Context, key, fingerprint string) (*IdemRecord, error) {
	pending, err := json.Marshal(IdemRecord{Fingerprint: fingerprint})
	if err != nil {
		return nil, err
	}
	// SET NX es atómico: si dos réplicas reciben la misma key a la vez,
	// solo una gana la reserva.
	ok, err := s.rdb.SetNX(ctx, idemKey(key), pending, idemPendingTTL).Result()
	if err != nil {
		return nil, fmt.Errorf("reservando idempotency key: %w", err)
	}
	if ok {
		return nil, nil
	}

	raw, err := s.rdb.Get(ctx, idemKey(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		// Venció entre el SETNX y el GET: probar de nuevo.
		return s.IdemBegin(ctx, key, fingerprint)
	}
	if err != nil {
		return nil, fmt.Errorf("leyendo idempotency key: %w", err)
	}
	var rec IdemRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("decodificando idempotency key: %w", err)
	}
	return &rec, nil
}

// IdemSave guarda la respuesta final de key para devolverla en reintentos.
func (s *Store) IdemSave(ctx context.Context, key string, rec IdemRecord) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, idemKey(key), raw, idemTTL).Err()
}

// IdemRelease borra la reserva de key. Se usa cuando la request falló sin
// efectos (validación, error interno): reintentar con la misma key es
// seguro y tiene que volver a procesarse.
func (s *Store) IdemRelease(ctx context.Context, key string) error {
	return s.rdb.Del(ctx, idemKey(key)).Err()
}

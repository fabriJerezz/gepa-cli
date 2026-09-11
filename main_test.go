package main

import (
	"reflect"
	"testing"
)

func TestExtractRedisAddrFlagDefault(t *testing.T) {
	t.Setenv("REDIS_ADDR", "")

	addr, rest := extractRedisAddrFlag([]string{"player", "list"})

	if addr != defaultRedisAddr {
		t.Errorf("addr = %q, esperaba el default %q", addr, defaultRedisAddr)
	}
	if !reflect.DeepEqual(rest, []string{"player", "list"}) {
		t.Errorf("rest = %v, no debería haber tocado los argumentos", rest)
	}
}

func TestExtractRedisAddrFlagDesdeEnv(t *testing.T) {
	t.Setenv("REDIS_ADDR", "redis-env:6379")

	addr, _ := extractRedisAddrFlag([]string{"player", "list"})

	if addr != "redis-env:6379" {
		t.Errorf("addr = %q, esperaba el valor de REDIS_ADDR", addr)
	}
}

func TestExtractRedisAddrFlagPisaEnv(t *testing.T) {
	t.Setenv("REDIS_ADDR", "redis-env:6379")

	addr, rest := extractRedisAddrFlag([]string{"--redis-addr", "redis-flag:6379", "player", "list"})

	if addr != "redis-flag:6379" {
		t.Errorf("addr = %q, el flag --redis-addr debería ganarle a la env var", addr)
	}
	if !reflect.DeepEqual(rest, []string{"player", "list"}) {
		t.Errorf("rest = %v, el flag y su valor deberían haberse removido", rest)
	}
}

func TestExtractRedisAddrFlagEnCualquierPosicion(t *testing.T) {
	addr, rest := extractRedisAddrFlag([]string{"team", "--redis-addr", "otro:6379", "add", "Argentina"})

	if addr != "otro:6379" {
		t.Errorf("addr = %q, esperaba %q", addr, "otro:6379")
	}
	if !reflect.DeepEqual(rest, []string{"team", "add", "Argentina"}) {
		t.Errorf("rest = %v, no coincide con lo esperado", rest)
	}
}

func TestInstanceNameDesdeEnv(t *testing.T) {
	t.Setenv("INSTANCE_NAME", "app2")

	if got := instanceName(); got != "app2" {
		t.Errorf("instanceName() = %q, esperaba %q", got, "app2")
	}
}

func TestInstanceNameFallbackAHostname(t *testing.T) {
	t.Setenv("INSTANCE_NAME", "")

	// Sin INSTANCE_NAME, debe caer al hostname del proceso (nunca vacío en
	// un entorno de test normal).
	if got := instanceName(); got == "" {
		t.Error("instanceName() no debería devolver vacío")
	}
}

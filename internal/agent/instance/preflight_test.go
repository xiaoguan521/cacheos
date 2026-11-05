package instance

import "testing"

func TestMinimumMaxMemoryBytesRoundsUpTenPercent(t *testing.T) {
	if got, want := MinimumMaxMemoryBytes(100), uint64(110); got != want {
		t.Fatalf("minimum = %d, want %d", got, want)
	}
	if got, want := MinimumMaxMemoryBytes(101), uint64(112); got != want {
		t.Fatalf("minimum = %d, want %d", got, want)
	}
	if err := ValidateMaxMemory(112, 101); err != nil {
		t.Fatalf("safe maxmemory rejected: %v", err)
	}
	if err := ValidateMaxMemory(111, 101); err == nil {
		t.Fatal("maxmemory below the rounded safety floor was accepted")
	}
}

func TestPersistencePreflight(t *testing.T) {
	const maxMemory = uint64(64 << 20)
	if got, want := PersistenceRequiredBytes(maxMemory, 32<<20), uint64(128<<20); got != want {
		t.Fatalf("required disk = %d, want %d", got, want)
	}
	if err := ValidatePersistenceDisk(128<<20, maxMemory, 32<<20); err != nil {
		t.Fatalf("sufficient disk rejected: %v", err)
	}
	if err := ValidatePersistenceDisk(128<<20-1, maxMemory, 32<<20); err == nil {
		t.Fatal("insufficient disk was accepted")
	}
	if !ParamsNeedPersistence(map[string]string{"appendonly": "yes"}) {
		t.Fatal("appendonly=yes was not recognized")
	}
	if !ParamsNeedPersistence(map[string]string{"save": "3600 1"}) {
		t.Fatal("RDB save schedule was not recognized")
	}
	if ParamsNeedPersistence(map[string]string{"appendonly": "no", "save": ""}) {
		t.Fatal("disabled persistence was recognized")
	}
}

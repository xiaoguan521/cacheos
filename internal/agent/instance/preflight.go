package instance

import (
	"fmt"
	"math"
	"strings"

	"github.com/xiaoguan521/cacheos/internal/paramcat"
)

const (
	// A persistence rewrite needs the current dataset and a second copy while
	// the temporary RDB/AOF is being built. Keep this conservative and shared
	// by the console precheck and the agent-side safety check.
	persistenceCopies = uint64(2)
	percentSafetyNum  = uint64(11)
	percentSafetyDen  = uint64(10)
)

// MinimumMaxMemoryBytes returns the smallest maxmemory that keeps a 10% safety
// margin above used_memory. It rounds up so a value just below the threshold is
// never accepted because of integer truncation.
func MinimumMaxMemoryBytes(usedMemory uint64) uint64 {
	if usedMemory == 0 {
		return 0
	}
	margin := usedMemory / percentSafetyDen
	if usedMemory%percentSafetyDen != 0 {
		margin++
	}
	if usedMemory > math.MaxUint64-margin {
		return math.MaxUint64
	}
	return usedMemory + margin
}

// PersistenceRequiredBytes returns the conservative free-space requirement for
// a persistence-enabled instance. The larger of maxmemory and used_memory is
// kept twice: one live copy and one rewrite temporary copy.
func PersistenceRequiredBytes(maxMemory, usedMemory uint64) uint64 {
	footprint := maxMemory
	if usedMemory > footprint {
		footprint = usedMemory
	}
	if footprint > math.MaxUint64/persistenceCopies {
		return math.MaxUint64
	}
	return footprint * persistenceCopies
}

// ValidateMaxMemory enforces the resize guard before CONFIG SET can lower the
// limit under the currently used dataset.
func ValidateMaxMemory(maxMemory, usedMemory uint64) error {
	if maxMemory == 0 {
		return fmt.Errorf("maxmemory must be greater than zero")
	}
	min := MinimumMaxMemoryBytes(usedMemory)
	if maxMemory < min {
		return fmt.Errorf("maxmemory %d bytes is below used_memory safety floor %d bytes", maxMemory, min)
	}
	return nil
}

// ValidatePersistenceDisk checks the free space available to the persistence
// directory. A zero value is treated as unknown and therefore unsafe when
// persistence is enabled.
func ValidatePersistenceDisk(freeBytes, maxMemory, usedMemory uint64) error {
	need := PersistenceRequiredBytes(maxMemory, usedMemory)
	if freeBytes < need {
		return fmt.Errorf("persistence directory has %d free bytes, need at least %d", freeBytes, need)
	}
	return nil
}

// ParamsNeedPersistence reports whether the effective configuration writes an
// RDB or AOF file under the instance directory.
func ParamsNeedPersistence(params map[string]string) bool {
	return strings.EqualFold(strings.TrimSpace(params["appendonly"]), "yes") || strings.TrimSpace(params["save"]) != ""
}

// MaxMemoryBytes parses the normalized maxmemory parameter. It is shared by
// console-side placement and agent-side command validation.
func MaxMemoryBytes(params map[string]string) (uint64, error) {
	v, ok := params["maxmemory"]
	if !ok || strings.TrimSpace(v) == "" {
		return 0, fmt.Errorf("maxmemory is required for a managed instance")
	}
	n, err := paramcat.ParseMemory(v)
	if err != nil || n <= 0 {
		if err != nil {
			return 0, err
		}
		return 0, fmt.Errorf("maxmemory must be greater than zero")
	}
	return uint64(n), nil
}

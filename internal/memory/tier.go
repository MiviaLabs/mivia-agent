package memory

import (
	"errors"
	"fmt"
)

// CoreTierCap bounds the number of core entries in one scope and org bucket.
const CoreTierCap = 24

// ErrEntryNotFound means that no entry with the requested ID exists.
var ErrEntryNotFound = errors.New("memory: entry not found")

// ErrCoreTierFull means that the core tier has reached its cap.
var ErrCoreTierFull = fmt.Errorf("memory: core tier is full (max %d entries)", CoreTierCap)

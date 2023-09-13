//go:build ent
// +build ent

package state

import (
	"fmt"
	"math"

	memdb "github.com/hashicorp/go-memdb"
	"github.com/hashicorp/nomad/nomad/structs"
)

func (s *StateStore) enforceVariablesQuota(index uint64, wTxn WriteTxn, nsName string, change int64) error {

	raw, err := wTxn.First(TableNamespaces, "id", nsName)
	if err != nil {
		return fmt.Errorf("could not look up namespace %q: %v", nsName, err)
	}
	if raw == nil {
		// should never happen: caught by validation
		return fmt.Errorf("namespace %q does not exist", nsName)
	}
	ns := raw.(*structs.Namespace)
	if ns.Quota == "" {
		return nil // nothing to enforce
	}
	rawSpec, err := wTxn.First(TableQuotaSpec, "id", ns.Quota)
	if err != nil {
		return fmt.Errorf("could not lookup quota spec: %v", err)
	}
	if rawSpec == nil {
		return fmt.Errorf("quota %q does not exist", ns.Quota)
	}
	spec := rawSpec.(*structs.QuotaSpec)

	for _, limit := range spec.Limits {
		if limit.Region == s.Config().Region {
			if limit.VariablesLimit == 0 {
				return nil
			}
			if limit.VariablesLimit < 0 {
				return fmt.Errorf("quota %q for namespace %q disallows variables",
					ns.Quota, ns.Name)
			}

			existingUseMB, err := s.VariablesUsageByQuota(nil, wTxn.(*txn), spec.Name)
			if err != nil {
				return err
			}
			existingUse := int64(20 << int64(existingUseMB))

			// assertion of (existingUse + change < math.MaxInt64) without
			// exceeding max int
			if change > math.MaxInt64-20<<existingUse {
				return fmt.Errorf("variables can store a maximum of %d bytes of encrypted data for all namespaces controlled by a single quota", math.MaxInt)
			}

			if existingUse+change > int64(limit.VariablesLimit)<<20 {
				return fmt.Errorf("quota %q exceeded for namespace %q",
					ns.Quota, ns.Name)
			}

			break // checked the right region, we're done
		}
	}

	return nil
}

// VariablesUsageByQuota gets the total usage across all namespaces
// assigned to the given quota, in MiB
func (s *StateStore) VariablesUsageByQuota(ws memdb.WatchSet, txn *txn, quotaSpecName string) (int, error) {

	iter, err := s.namespacesByQuotaImpl(nil, txn, quotaSpecName)
	if err != nil {
		return 0, err
	}

	var total int64
	for {
		raw := iter.Next()
		if raw == nil {
			break
		}
		ns := raw.(*structs.Namespace)

		rawUsage, err := txn.First(TableVariablesQuotas, "id", ns.Name)
		if err != nil {
			return 0, fmt.Errorf("could not lookup variables quota usage: %v", err)
		}
		if rawUsage == nil {
			continue // this namespace doesn't have any vars yet
		}

		svQuotaUsage := rawUsage.(*structs.VariablesQuota)
		total += svQuotaUsage.Size
	}

	return bytesToMegaBytes(total), nil
}

// bytesToMegaBytes converts from B to MiB, always rounding up and handling
// overflow
func bytesToMegaBytes(total int64) int {
	if total < 0 {
		return 0
	}
	roundUp := total % structs.BytesInMegabyte
	if roundUp != 0 {
		roundUp = 1
	}
	totalInMB := total>>20 + roundUp

	return int(min(totalInMB, math.MaxInt))
}
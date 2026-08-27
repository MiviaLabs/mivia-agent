//go:build ledger_sqlite

package sdkadapter

// DisplayName returns the CLI's display-name field for an SDK-shaped
// ledger entry. It is a stub until the first caller in B.2 #9 lands;
// the SDK's ledger entry shape will gain a DisplayName field at that
// point and this function becomes the one-line bridge the policy
// describes.
//
// B.2 #9 follow-up: replace this stub with a CLI<->SDK display-name
// converter once the SDK ledger entry carries DisplayName.
func DisplayName() string {
	return ""
}

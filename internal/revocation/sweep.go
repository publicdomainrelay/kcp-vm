package revocation

type PLCResolution struct {
	Found bool

	Err error

	Keys []string
}

func Sweep(r PLCResolution) (revoke bool, why string) {
	switch {
	case r.Err != nil:
		return false, "the directory is unreachable: an outage is not a deletion"
	case !r.Found:
		return true, "the directory answered 404: the account is gone"
	case len(r.Keys) == 0:
		return true, "the document carries no verification key"
	default:
		return false, "the document is live"
	}
}

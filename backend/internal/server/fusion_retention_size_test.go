package server

import "testing"

// A claim whose provisioner does not enforce the requested size (the local-path one) is told apart from one that does.
func TestSizeNotEnforcedNamesTheProvisionersThatIgnoreTheRequestedSize(t *testing.T) {
	if !sizeNotEnforced("rancher.io/local-path") || sizeNotEnforced("ebs.csi.aws.com") {
		t.Fatal("the local-path provisioner ignores the size and the EBS one does not")
	}
}

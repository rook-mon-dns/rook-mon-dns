{ pkgs ? import <nixpkgs> { } }:

pkgs.mkShell {
  packages = with pkgs; [
    go
    kubebuilder
    kustomize
    kubeconform
    kubectl
    kubernetes-helm
    gnumake
    git
  ];

  # Go caches live OUTSIDE the project tree: controller-gen's "./..." package
  # resolution descends into directories under the project root and would treat
  # the module cache as package roots. Tools themselves come from /nix/store
  # and survive reboots; after a /tmp wipe a plain `go mod download` suffices.
  shellHook = ''
    export GOPATH=/tmp/opencode/gopath
    export GOCACHE=/tmp/opencode/gocache
    mkdir -p "$GOPATH" "$GOCACHE"
  '';
}

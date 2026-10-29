{ pkgs ? import <nixpkgs> { } }:

pkgs.mkShell {
  packages = with pkgs; [
    go
    kubebuilder
    kustomize
    kubeconform
    kubectl
    kind
    kubernetes-helm
    gnumake
    git
  ];
}

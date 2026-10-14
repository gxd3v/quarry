# Publicar uma versão

## 1. Release

```sh
git tag v1.0.0
git push origin v1.0.0
```

A tag dispara o workflow `.github/workflows/release.yml`, que faz o seguinte:

| Job | Gera |
|---|---|
| `linux` (ubuntu-24.04) | `quarry-linux-amd64.tar.gz`, `.deb`, `.rpm` (com o nfpm) e `Quarry-x86_64.AppImage` |
| `windows` | `quarry-windows-amd64.zip` (portátil, também usado pelo Scoop) e `quarry-windows-amd64-installer.exe` (NSIS, usado pelo winget) |
| `macos` | `quarry-macos-universal.zip` com o `Quarry.app` universal (amd64 e arm64) |
| `publish` | `SHA256SUMS` e os manifestos preenchidos com a versão e os checksums (`scripts/render-manifests.sh`); cria uma **release em rascunho** com tudo |

A versão vem da tag e fica embutida com `-ldflags "-X main.version=…"`. `quarry -version` mostra-a, e é com ela que a app procura atualizações. As builds de desenvolvimento (`dev`) nunca procuram.

Revê o rascunho no GitHub e publica-o. A verificação de atualizações da app lê `releases/latest`, que ignora rascunhos e pré-releases.

## 2. Gestores de pacotes

Os manifestos gerados vão anexados à release. Publicá-los precisa de contas e tokens teus, por isso o workflow não o faz:

| Gestor | Ficheiro | Onde publicar |
|---|---|---|
| AUR | `PKGBUILD` | Pacote `quarry-bin` no AUR. Fazer `makepkg --printsrcinfo > .SRCINFO` e push por SSH para `aur@aur.archlinux.org:quarry-bin.git`. |
| Homebrew | `quarry.rb` (cask) | Copiar para `Casks/quarry.rb` num repositório `gxd3v/homebrew-tap`. Os utilizadores instalam com `brew install --cask gxd3v/tap/quarry`. |
| Scoop | `quarry.json` | Copiar para `bucket/quarry.json` num repositório `gxd3v/scoop-bucket`. O `checkver`/`autoupdate` já está configurado. |
| winget | `gxd3v.Quarry.yaml` | Submeter com `wingetcreate submit` (ou num PR a `microsoft/winget-pkgs`, em `manifests/g/gxd3v/Quarry/<versão>/`). |

Quando existirem o tap e o bucket, os passos de cópia podem passar para o workflow, usando um token guardado nos secrets.

## 3. Limitações conhecidas

- **Linux:** o AppImage não inclui o WebKitGTK. Precisa de `libwebkit2gtk-4.1` no sistema, como o `.deb` e o `.rpm`.
- **macOS:** a app não está assinada nem notarizada, por isso o Gatekeeper avisa na primeira abertura. Para assinar é preciso um certificado Apple Developer.
- **Windows:** o instalador não está assinado e o SmartScreen pode avisar.

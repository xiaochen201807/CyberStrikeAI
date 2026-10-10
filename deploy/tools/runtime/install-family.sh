#!/bin/sh
set -eu

# Build one independently deployable runtime image for a logical tool family.
# The image is intentionally assembled from the Kali headless base and a
# family-specific package set; it is not a catch-all scanner image.  The
# final Dockerfile step runs toolcatalog doctor so a missing executable fails
# the image build instead of being discovered after deployment.

family=${1:-}
case "$family" in
  binary-forensics|cloud|exploitation|external-intel|identity|recon|utility|web) ;;
  *) echo "unsupported TOOL_FAMILY: $family" >&2; exit 2 ;;
esac

export DEBIAN_FRONTEND=noninteractive

apt-get update

apt_install_available() {
  available=""
  for package in "$@"; do
    if apt-cache show "$package" >/dev/null 2>&1; then
      available="$available $package"
    else
      echo "package not present in the configured Kali repositories: $package" >&2
    fi
  done
  if [ -n "$available" ]; then
    # shellcheck disable=SC2086
    apt-get install -y --no-install-recommends $available
  fi
}

pip_install_optional() {
  for package in "$@"; do
    if "$python_venv/bin/pip" install --no-cache-dir "$package"; then
      continue
    fi
    echo "pip package failed to install: $package" >&2
    return 1
  done
}

gem_install_optional() {
  for package in "$@"; do
    if gem install --no-document "$package"; then
      continue
    fi
    echo "gem failed to install: $package" >&2
    return 1
  done
}

go_install_pinned() {
  module=$1
  version=$2
  GOBIN=/usr/local/bin go install "$module@$version"
}

download_binary() {
  url=$1
  output=$2
  expected_sha256=$3
  tmp=$(mktemp)
  curl -fsSL --retry 3 --retry-delay 2 "$url" -o "$tmp"
  printf '%s  %s\n' "$expected_sha256" "$tmp" | sha256sum -c -
  install -m 0755 "$tmp" "$output"
}

install_zip_binary() {
  url=$1
  member=$2
  output=$3
  expected_sha256=$4
  tmp=$(mktemp)
  curl -fsSL --retry 3 --retry-delay 2 "$url" -o "$tmp"
  printf '%s  %s\n' "$expected_sha256" "$tmp" | sha256sum -c -
  unzip -p "$tmp" "$member" > "$output"
  chmod 0755 "$output"
}

install_tar_binary() {
  url=$1
  member=$2
  output=$3
  expected_sha256=$4
  tmp=$(mktemp)
  curl -fsSL --retry 3 --retry-delay 2 "$url" -o "$tmp"
  printf '%s  %s\n' "$expected_sha256" "$tmp" | sha256sum -c -
  tar -xzf "$tmp" -O "$member" > "$output"
  chmod 0755 "$output"
}

# Common libraries are deliberately small and shared by all family images.
apt_install_available \
  ca-certificates curl wget git jq unzip xz-utils file procps \
  python3 python3-pip python3-venv python3-dev \
  build-essential pkg-config libffi-dev libssl-dev libpcap-dev \
  ruby ruby-dev npm nodejs golang-go cargo rustc binutils gnupg

# Keep Python tooling out of the distribution-managed site-packages tree.  A
# number of security tools depend on newer packaging metadata than Kali ships;
# installing them in a dedicated venv avoids pip trying to uninstall apt-owned
# files while keeping their console entry points on PATH at runtime.
python_venv=/opt/cyberstrike-venv
python3 -m venv "$python_venv"
"$python_venv/bin/pip" install --no-cache-dir --upgrade pip setuptools wheel
export PATH="$python_venv/bin:$PATH"

case "$family" in
  recon)
    apt_install_available \
      amass arp-scan dnsenum fierce masscan nbtscan nmap rustscan subfinder \
      dnsrecon dnsutils
    # These projects publish reproducible release tags but are not present in
    # every Kali snapshot.  Pin versions so a rebuild does not silently float.
    go_install_pinned github.com/lc/gau/v2/cmd/gau v2.2.4
    go_install_pinned github.com/tomnomnom/waybackurls v0.1.0
    go_install_pinned github.com/projectdiscovery/katana/cmd/katana v1.4.0
    ;;
  web)
    apt_install_available \
      arjun dalfox dirsearch dotdotpwn ffuf gobuster graphqlmap nikto nuclei \
      paramspider sqlmap wafw00f wpscan xsser zaproxy
    git clone --depth 1 https://github.com/ticarpi/jwt_tool.git /opt/jwt_tool
    "$python_venv/bin/pip" install --no-cache-dir -r /opt/jwt_tool/requirements.txt
    cat > /usr/local/bin/jwt_tool <<'EOF'
#!/bin/sh
exec /opt/cyberstrike-venv/bin/python /opt/jwt_tool/jwt_tool.py "$@"
EOF
    chmod 0755 /usr/local/bin/jwt_tool
    git clone --depth 1 https://github.com/swisskyrepo/GraphQLmap.git /opt/graphqlmap
    # GraphQLmap's historical `readline` pin does not build on Python 3.14;
    # GNU readline is already provided by the base image and is not needed as
    # a Python dependency for its non-interactive MCP invocation.
    grep -Ev '^[[:space:]]*(py)?readline([[:space:];<=>].*)?$' /opt/graphqlmap/requirements.txt \
      > /tmp/graphqlmap-requirements.txt
    "$python_venv/bin/pip" install --no-cache-dir -r /tmp/graphqlmap-requirements.txt
    cat > /usr/local/bin/graphqlmap <<'EOF'
#!/bin/sh
exec /opt/cyberstrike-venv/bin/python /opt/graphqlmap/bin/graphqlmap "$@"
EOF
    chmod 0755 /usr/local/bin/graphqlmap
    cargo install --root /usr/local --locked --version 4.3.2 x8
    # jaeles is distributed as a static release instead of a Go module.
    install_zip_binary \
      https://github.com/jaeles-project/jaeles/releases/download/beta-v0.17.1/jaeles-beta-v0.17.1-linux.zip \
      jaeles /usr/local/bin/jaeles \
      8553eea0bad67fa65d9a6831a4ffcd6860b488f922babbf1005288a8f2ad39e3
    ;;
  identity)
    apt_install_available \
      bloodhound enum4linux-ng hydra impacket-scripts python3-impacket \
      netexec responder smbmap samba-common-bin smbclient
    ;;
  binary-forensics)
    apt_install_available \
      binwalk checksec libimage-exiftool-perl foremost gdb ghidra hashcat \
      john radare2 ropper steghide xxd python3-pwntools python3-capstone \
      liblzma-dev
    pip_install_optional angr volatility3 ROPGadget==7.6
    gem_install_optional one_gadget zsteg
    cargo install --root /usr/local --locked --version 3.3.3 pwninit
    git clone --depth 1 https://github.com/mheistermann/HashPump-partialhash.git /opt/hashpump
    make -C /opt/hashpump
    install -m 0755 /opt/hashpump/hashpump /usr/local/bin/hashpump
    # The libc-database command is a shell/Python repository rather than a
    # package.  Keep it pinned and expose its stable wrapper name.
    git clone --depth 1 https://github.com/niklasb/libc-database.git /opt/libc-database
    cat > /usr/local/bin/libc-database <<'EOF'
#!/bin/sh
set -eu
action=${1:-}
[ -n "$action" ] || { echo "usage: libc-database {find|dump|get|download} ..." >&2; exit 2; }
shift
case "$action" in
  find|dump|get|download) exec "/opt/libc-database/$action" "$@" ;;
  *) echo "unsupported libc-database action: $action" >&2; exit 2 ;;
esac
EOF
    chmod 0755 /usr/local/bin/libc-database
    # Volatility3 publishes vol.py; CyberStrikeAI's YAML contract uses the
    # stable volatility3 command name.
    if command -v vol >/dev/null 2>&1 && [ ! -e /usr/local/bin/volatility3 ]; then
      ln -s "$(command -v vol)" /usr/local/bin/volatility3
    elif command -v vol.py >/dev/null 2>&1 && [ ! -e /usr/local/bin/volatility3 ]; then
      ln -s "$(command -v vol.py)" /usr/local/bin/volatility3
    fi
    if ! command -v analyzeHeadless >/dev/null 2>&1; then
      ghidra_headless=$(find /opt /usr/share -type f -name analyzeHeadless -perm -0111 2>/dev/null | head -1 || true)
      if [ -n "$ghidra_headless" ]; then
        ln -s "$ghidra_headless" /usr/local/bin/analyzeHeadless
      fi
    fi
    ;;
  cloud)
    # Falco is maintained in the vendor repository rather than Kali. The
    # package is installed with its driver disabled because this container is
    # deliberately unprivileged; a dedicated privileged runner is required
    # for kernel event collection at execution time.
    if ! apt-cache show falco >/dev/null 2>&1; then
      curl -fsSL https://falco.org/repo/falcosecurity-packages.asc \
        | gpg --dearmor -o /usr/share/keyrings/falco-archive-keyring.gpg
      printf '%s\n' \
        'deb [signed-by=/usr/share/keyrings/falco-archive-keyring.gpg] https://download.falco.org/packages/deb stable main' \
        > /etc/apt/sources.list.d/falcosecurity.list
      apt-get update
    fi
    FALCO_FRONTEND=noninteractive FALCO_DRIVER_CHOICE=none FALCOCTL_ENABLED=no \
      apt_install_available falco
    apt_install_available trivy awscli jq python3-tk autoconf automake libtool
    # Prowler 5.x currently requires Python <3.14; Kali rolling ships 3.14.
    # Keep the latest 3.x release that supports the image interpreter until
    # the family base moves to a compatible Python runtime.
    pip_install_optional \
      checkov==3.3.26 kube-hunter==0.6.8 \
      prowler==3.11.3 scoutsuite==5.14.0
    # CloudMapper is not consistently published to PyPI; retain the pinned
    # upstream checkout when a distribution is unavailable.
    if ! command -v cloudmapper >/dev/null 2>&1; then
      git clone --depth 1 https://github.com/duo-labs/cloudmapper.git /opt/cloudmapper
      "$python_venv/bin/pip" install --no-cache-dir -r /opt/cloudmapper/requirements.txt
      cat > /usr/local/bin/cloudmapper <<'EOF'
#!/bin/sh
exec /opt/cyberstrike-venv/bin/python /opt/cloudmapper/cloudmapper.py "$@"
EOF
      chmod 0755 /usr/local/bin/cloudmapper
    fi
    # Releases are checksummed by the upstream project and avoid a Go toolchain
    # dependency in the image.
    install_tar_binary \
      https://github.com/aquasecurity/kube-bench/releases/download/v0.16.0/kube-bench_0.16.0_linux_amd64.tar.gz \
      kube-bench /usr/local/bin/kube-bench \
      82dbc7e598740dc9344d41f8ad0b8210d57c4c00bdb2c5f1d8a69a2b98baddcf
    # Terrascan ships a tarball with a stable checksum.
    tmp=$(mktemp)
    curl -fsSL --retry 3 --retry-delay 2 \
      https://github.com/tenable/terrascan/releases/download/v1.19.9/terrascan_1.19.9_Linux_x86_64.tar.gz -o "$tmp"
    printf '%s  %s\n' \
      9275d75210b7df52f8870252035803288646674f5701ced82812037700a39670 \
      "$tmp" | sha256sum -c -
    tar -xzf "$tmp" -C /usr/local/bin terrascan
    chmod 0755 /usr/local/bin/terrascan
    ;;
  exploitation)
    apt_install_available metasploit-framework msfvenom peass
    if [ ! -e /usr/local/bin/linpeas.sh ]; then
      curl -fsSL --retry 3 --retry-delay 2 \
        https://github.com/peass-ng/PEASS-ng/releases/latest/download/linpeas.sh \
        -o /usr/local/bin/linpeas.sh
      chmod 0755 /usr/local/bin/linpeas.sh
    fi
    ;;
  external-intel)
    apt_install_available python3-requests python3-httpx
    ;;
  utility)
    npm install --global --no-fund --no-audit @stoplight/spectral-cli@6.15.0
    git clone --depth 1 https://github.com/niklasb/libc-database.git /opt/libc-database
    cat > /usr/local/bin/libc-database <<'EOF'
#!/bin/sh
set -eu
action=${1:-}
[ -n "$action" ] || { echo "usage: libc-database {find|dump|get|download} ..." >&2; exit 2; }
shift
case "$action" in
  find|dump|get|download) exec "/opt/libc-database/$action" "$@" ;;
  *) echo "unsupported libc-database action: $action" >&2; exit 2 ;;
esac
EOF
    chmod 0755 /usr/local/bin/libc-database
    ;;
esac

# Remove package metadata and build caches.  Do not remove /opt sources: a
# number of tool contracts intentionally invoke their upstream wrapper.
rm -rf /var/lib/apt/lists/* /root/.cache /root/.cargo/registry /root/.cargo/git


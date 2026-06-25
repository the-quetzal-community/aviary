#!/bin/bash
# Builds the sentry-godot stack as LIBC++ static archives in an Alpine container and
# drops them in internal/sentry/lib/, where the cgo package (sentry_musl.go) static-links
# them into aviary's musl binary. The archives compile against libc++ (matching aviary's
# zig `-lc++`) but DON'T bundle it — the final musl link resolves the libc++ symbols.
#
# Why static-link instead of dlopen'ing a .so on musl: graphics.gd's static-musl loader
# borrows the *host's* dynamic loader, so a foreign .so is host-libc-bound. Linking the
# extension in keeps everything inside the single self-contained binary.
#
# Native crash capture uses the inproc backend (NOT crashpad — its handler can't run in a
# fully-static binary; see the SENTRY_BACKEND note below). No sidecar exe, fully portable.
#
# Requires: podman. Output: libc++ .a archives in ./lib (gitignored).
set -euo pipefail

TAG="1.6.0"   # keep in sync with fetch_sentry.sh
PKG="$(cd "$(dirname "$0")" && pwd)"                 # internal/sentry
SRC="${SENTRY_GODOT_SRC:-$HOME/git/sentry-godot}"
OUT="$PKG/lib"

if [ ! -f "$SRC/SConstruct" ]; then
  git clone https://github.com/getsentry/sentry-godot "$SRC"
fi
git -C "$SRC" fetch --depth 1 origin "+refs/tags/$TAG:refs/tags/$TAG"
git -C "$SRC" checkout -q "$TAG"
git -C "$SRC" submodule update --init --recursive --depth 1

INNER="$(mktemp)"
trap 'rm -f "$INNER"' EXIT
cat > "$INNER" <<'INNERSCRIPT'
#!/bin/sh
# Build the sentry-godot stack as LIBC++ static archives. Compiles against libc++ but
# does NOT bundle it — aviary's own zig libc++ resolves the symbols at the final link.
set -e
apk add --no-cache cmake samurai scons make binutils git linux-headers musl-dev perl \
    clang lld llvm libc++ libc++-dev libc++-static llvm-libunwind-dev llvm-libunwind-static compiler-rt \
    openssl-dev openssl-libs-static zlib-dev zlib-static wget xz >/dev/null
git config --global --add safe.directory '*'
echo "clang: $(clang --version | head -1)"

# minimal static curl (C — runtime-agnostic) for sentry-native's transport at final link
cd /tmp
wget -q https://curl.se/download/curl-8.11.1.tar.xz && tar xf curl-8.11.1.tar.xz
cd curl-8.11.1
CC=clang ./configure --prefix=/usr --disable-shared --enable-static --with-openssl --with-zlib \
  --without-libpsl --without-libidn2 --without-brotli --without-zstd --without-nghttp2 --without-libssh2 \
  --disable-ldap --disable-ldaps --disable-rtsp --disable-dict --disable-telnet --disable-tftp \
  --disable-pop3 --disable-imap --disable-smtp --disable-gopher --disable-mqtt --disable-manual --disable-dependency-tracking >/dev/null
make -j"$(nproc)" >/dev/null 2>&1 && make install >/dev/null 2>&1
rm -f /usr/lib/libcurl.so* /usr/lib/libz.so /usr/lib/libssl.so /usr/lib/libcrypto.so

cd /src
git checkout -q modules/sentry-native.SConscript SConstruct src/sentry/native/native_sdk.cpp 2>/dev/null || true
git -C modules/godot-cpp checkout -q tools/linux.py 2>/dev/null || true
rm -rf modules/sentry-native/build modules/sentry-native/install /src/out && mkdir -p /src/out
# Drop stale GCC/libstdc++ objects so everything recompiles under clang+libc++.
find modules/godot-cpp src project/addons/sentry/bin -name "*.o" -delete 2>/dev/null || true
rm -f modules/godot-cpp/bin/*.a project/addons/sentry/bin/linux/x86_64/*.a

python3 - <<'PY'
# sentry-native cmake: clang + libc++ (-stdlib=libc++ must fold INTO the x86_64
# -DCMAKE_CXX_FLAGS line — cmake honours the LAST -DCMAKE_CXX_FLAGS).
f="modules/sentry-native.SConscript"; s=open(f).read()
old='cmake_gen += \' -DCMAKE_C_FLAGS="-m64" -DCMAKE_CXX_FLAGS="-m64" -DLINK_OPTIONS="-m64"\''
# SENTRY_BACKEND=inproc — NOT crashpad. crashpad needs a separate handler exe that
# interposes pthread_create via dlsym(RTLD_NEXT), which can't resolve in a fully-static
# binary (it aborts: "dlsym: Symbol not found: pthread_create"). inproc handles crashes
# in-process: no handler exe, no dlsym, works fully static + portable on musl AND glibc.
# This is the static-musl build only; the upstream addon (Windows/glibc) keeps crashpad.
new='cmake_gen += \' -DCMAKE_C_COMPILER=clang -DCMAKE_CXX_COMPILER=clang++ -DSENTRY_BACKEND=inproc -DCMAKE_C_FLAGS="-m64" -DCMAKE_CXX_FLAGS="-m64 -stdlib=libc++" -DCMAKE_EXE_LINKER_FLAGS="-stdlib=libc++ -fuse-ld=lld" -DLINK_OPTIONS="-m64" -DCURL_USE_STATIC_LIBS=ON\''
assert old in s, "x86_64 cmake flags not found"; s=s.replace(old,new)
guard='''if env.get("use_llvm") == True:
    print("ERROR: Compiling with LLVM is not supported yet.")
    Exit(1)'''
assert guard in s, "llvm guard not found"; s=s.replace(guard, "# use_llvm allowed: cmake_gen sets clang+libc++ explicitly")
# inproc backend: the SConscript hardcodes crashpad libs/handler — none exist under
# SENTRY_BACKEND=inproc, so keep only libsentry + the vendored libunwind and drop the
# crashpad_handler target + its `cmake --build --target crashpad_handler` step.
a='''def add_lib_target(lib_name):
    if platform == "windows":'''
assert a in s, "add_lib_target not found"
s=s.replace(a, '''def add_lib_target(lib_name):
    if lib_name not in ("sentry", "unwind"):
        return  # inproc: no crashpad/mini_chromium libs
    if platform == "windows":''')
b='''else:
    build_targets.append(File("sentry-native/install/bin/crashpad_handler"))'''
assert b in s, "crashpad_handler target not found"
s=s.replace(b, '''else:
    pass  # inproc: no crashpad_handler''')
c='        f"cd {sentry_native_path} && {cmake_crashpad}",\n'
assert c in s, "cmake_crashpad action not found"
s=s.replace(c, '')
# Force inproc at the base cmake_gen line (cmake -D last-wins is unreliable, so the
# default "-DSENTRY_BACKEND=crashpad" was sticking even with our inproc on the x86_64 line).
bk='cmake_gen += " -DSENTRY_BACKEND=crashpad"'
assert bk in s, "backend line not found"
s=s.replace(bk, 'cmake_gen += " -DSENTRY_BACKEND=inproc"')
open(f,"w").write(s)
# SConstruct: emit sentry-godot as a static archive (alongside its .so) for cgo linking.
f2="SConstruct"; s2=open(f2).read()
ol='''    lib_name = f"libsentry.{platform}.{build_type}.{arch}{extra}{shlib_suffix}"
    lib_path = f"{out_dir}/{lib_name}"

    library = env.SharedLibrary(lib_path, source=sources)
    Default(library)'''
nl=ol+'''
    if platform == "linux":
        Default(env.StaticLibrary(f"{out_dir}/libsentry_godot.{platform}.{build_type}.{arch}.a", source=sources))'''
assert ol in s2, "library block not found"; s2=s2.replace(ol,nl)
# inproc backend: drop the main SConstruct's crashpad_handler deploy (its source — a built
# crashpad_handler — doesn't exist under inproc, so this step would fail "Source not found").
cp='''    # Deploy crashpad handler to project directory.
    deploy_crashpad_handler = env.CopyCrashpadHandler(out_dir)
    Default(deploy_crashpad_handler)

    if env["separate_debug_symbols"] and platform == "linux":
        handler_path = str(deploy_crashpad_handler[0])
        symbols_path = f"{handler_path}.debug"
        Default(env.SeparateDebugSymbols(File(symbols_path), File(handler_path)))'''
assert cp in s2, "CopyCrashpadHandler block not found"
s2=s2.replace(cp, "    # inproc backend: no crashpad_handler to deploy.")
open(f2,"w").write(s2)
# godot-cpp: compile with libc++ under use_llvm (default linux clang uses libstdc++).
f3="modules/godot-cpp/tools/linux.py"; s3=open(f3).read()
ol3='''    if env["use_llvm"]:
        clang.generate(env)
        clangxx.generate(env)'''
nl3=ol3+'''
        env.Append(CXXFLAGS=["-stdlib=libc++"])
        env.Append(LINKFLAGS=["-stdlib=libc++"])'''
assert ol3 in s3, "godot-cpp use_llvm block not found"; open(f3,"w").write(s3.replace(ol3,nl3))
# native_sdk.cpp assumes crashpad: when crashpad_handler isn't found it nulls the backend,
# which would also disable our inproc backend. Drop that else so inproc stays active (no
# handler needed) instead of printing "backend disabled" and capturing nothing.
f4="src/sentry/native/native_sdk.cpp"; s4=open(f4).read()
old4='\tif (FileAccess::file_exists(handler_path)) {\n\t\tsentry_options_set_handler_path(options, handler_path.utf8());\n\t} else {\n\t\tERR_PRINT(vformat("Sentry: Failed to locate crash handler (crashpad) - backend disabled (%s)", handler_path));\n\t\tsentry_options_set_backend(options, NULL);\n\t}'
assert old4 in s4, "native_sdk handler else not found"
s4=s4.replace(old4, '\tif (FileAccess::file_exists(handler_path)) {\n\t\tsentry_options_set_handler_path(options, handler_path.utf8());\n\t}\n\t// inproc backend: no crashpad_handler needed; keep the default in-process backend.')
open(f4,"w").write(s4)
print("patched: cmake clang+libc++, sentry-godot StaticLibrary, godot-cpp libc++, native_sdk inproc")
PY

# use_llvm=yes → clang; use_static_cpp=no drops -static-libstdc++; linux.py adds -stdlib=libc++.
scons platform=linux arch=x86_64 target=template_release use_llvm=yes use_static_cpp=no -j"$(nproc)" \
  > /src/out/scons.log 2>&1 || true
echo "--- scons summary (errors/done) ---"
grep -iE "error:|fatal error|undefined|scons: \*\*\*|No such file|Linking Static|scons: done" /src/out/scons.log | tail -40

# Collect every archive the cgo package links (inproc backend → no crashpad libs, no handler exe).
LX=project/addons/sentry/bin/linux/x86_64
cp $LX/libsentry_godot.linux.release.x86_64.a /src/out/ 2>/dev/null || echo "MISSING libsentry_godot.a"
cp modules/godot-cpp/bin/libgodot-cpp.linux.template_release.x86_64.a /src/out/ 2>/dev/null || echo "MISSING godot-cpp.a"
cp modules/sentry-native/install/lib/*.a /src/out/ 2>/dev/null || echo "MISSING sentry-native libs"
cp /usr/lib/libcurl.a /usr/lib/libssl.a /usr/lib/libcrypto.a /usr/lib/libz.a /src/out/ 2>/dev/null || true

echo "=== verify C++ runtime (want libc++ __1, NOT libstdc++) ==="
for a in /src/out/libsentry_godot.linux.release.x86_64.a /src/out/libgodot-cpp.linux.template_release.x86_64.a \
         /src/out/libsentry.a; do
  [ -f "$a" ] && echo "$(basename $a): __1=$(nm "$a" 2>/dev/null|grep -c St3__1) libstdc++=$(nm "$a" 2>/dev/null|grep -E '_ZN(K?)St[0-9]'|grep -vc St3__1)"
done
echo "=== out (no crashpad_handler — inproc backend) ==="; ls -la /src/out/
INNERSCRIPT

podman run --rm -v "$SRC:/src" -v "$INNER:/build.sh:ro" docker.io/library/alpine:3.21 sh /build.sh

mkdir -p "$OUT"
rm -f "$OUT"/libcrashpad_*.a "$OUT"/libmini_chromium.a "$OUT"/crashpad_handler   # drop stale crashpad bits
cp "$SRC"/out/*.a "$OUT/"
echo "libc++ archives (inproc) -> $OUT"; ls "$OUT"

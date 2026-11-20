#!/usr/bin/env bash
set -euo pipefail

scratch="$RUNNER_TEMP/deskpulse-win11"
container=deskpulse-win11-test
image='docker.io/dockurr/windows@sha256:59f8bed19cfd49cf1701debe34f9df6311e61a4e2ae68aa9f41206e2184e31cc'
evaluation='https://software-static.download.prss.microsoft.com/dbazure/888969d5-f34g-4e03-ac9d-1f9786c66749/26100.1742.240906-0331.ge_release_svc_refresh_CLIENT_LTSC_EVAL_x64FRE_en-us.iso'
rdp_pid=''
xvfb_pid=''

cleanup() {
    local code=$?
    if [[ -n "$rdp_pid" ]]; then kill "$rdp_pid" 2>/dev/null || true; fi
    if [[ -n "$xvfb_pid" ]]; then kill "$xvfb_pid" 2>/dev/null || true; fi
    if docker container inspect "$container" >/dev/null 2>&1; then
        if ((code)); then docker logs --tail 30 "$container" 2>&1 || true; fi
        docker stop --time 20 "$container" >/dev/null 2>&1 || true
        docker rm "$container" >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT

test -c /dev/kvm
disk_available() { df -Pk "$RUNNER_TEMP" | awk 'NR==2 { print $4 }'; }
# This hosted runner is disposable. Its Android SDK is unused by this VM job.
if (( $(disk_available) < 35 * 1024 * 1024 )); then
    sudo rm -rf -- /usr/local/lib/android /opt/ghc
    sudo apt-get clean
fi
df -h "$RUNNER_TEMP"
if (( $(disk_available) < 30 * 1024 * 1024 )); then
    echo 'The hosted runner has less than 30 GiB of scratch space for the evaluation VM.' >&2
    exit 1
fi
sudo apt-get update -qq
sudo apt-get install --no-install-recommends -y -qq freerdp3-x11 xvfb

mkdir -p "$scratch/oem" "$scratch/shared" "$scratch/storage"
cp dist/deskpulse-agent.exe dist/deskpulse-agent-x64.msi "$scratch/oem/"
cp scripts/windows11-vm/install.bat scripts/windows11-vm/test.ps1 scripts/test-windows-render.ps1 "$scratch/oem/"
sed -i 's/\r\?$/\r/' "$scratch/oem/install.bat"
touch "$scratch/shared/host-ready.txt"
export PASSWORD="Aa9-$(openssl rand -hex 12)"
echo "::add-mask::$PASSWORD"
docker run --detach --name "$container" --device /dev/kvm --device /dev/net/tun --cap-add NET_ADMIN \
    --publish 127.0.0.1:13389:3389 \
    --volume "$scratch/storage:/storage" --volume "$scratch/oem:/oem:ro" --volume "$scratch/shared:/shared" \
    --env VERSION="$evaluation" --env USERNAME=DeskPulseQA --env PASSWORD \
    --env RAM_SIZE=5G --env CPU_CORES=3 --env DISK_FMT=qcow2 --env DISK_SIZE=80G --env TPM=Y \
    "$image" >/dev/null

wait_for() {
    local name="$1" deadline=$((SECONDS + $2))
    while [[ ! -s "$scratch/shared/$name" ]]; do
        if [[ -s "$scratch/shared/failed.json" ]]; then
            cat "$scratch/shared/failed.json" >&2
            return 1
        fi
        if ((SECONDS >= deadline)) || [[ $(docker inspect --format '{{.State.Running}}' "$container") != true ]]; then
            echo "The VM did not produce $name within the test window." >&2
            return 1
        fi
        sleep 4
    done
    cat "$scratch/shared/$name"
    echo
}

wait_for console.json 1350
Xvfb :91 -screen 0 1440x900x24 -nolisten tcp >"$scratch/xvfb.log" 2>&1 &
xvfb_pid=$!
sleep 2

connect() {
    DISPLAY=:91 xfreerdp3 /v:127.0.0.1:13389 /u:DeskPulseQA /p:"$PASSWORD" \
        /cert:tofu /size:"$1" -auto-reconnect /log-level:ERROR >"$scratch/rdp.log" 2>&1 &
    rdp_pid=$!
    sleep 3
    if ! kill -0 "$rdp_pid" 2>/dev/null; then
        cat "$scratch/rdp.log" >&2
        return 1
    fi
}

connect 1200x760
wait_for rdp-first.json 135
kill "$rdp_pid"
wait "$rdp_pid" || true
rdp_pid=''
wait_for rdp-disconnected.json 55
connect 1360x820
wait_for rdp-reconnected.json 95
wait_for passed.json 100

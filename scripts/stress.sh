#!/busybox/sh
# CPU stress for the HPA demo. Runs inside the app container:
#
#   kubectl -n sovereign-ai exec -it deploy/secure-intake -- /stress.sh [workers]
#   kubectl -n sovereign-ai exec -it deploy/secure-intake -- /stress.sh status
#   kubectl -n sovereign-ai exec -it deploy/secure-intake -- /stress.sh stop
#
# Each worker is a busy loop that keeps one CPU core at 100% until stopped
# (or until the pod is deleted). Workers keep running after the exec session
# ends. One worker is already ~10x the pod's 100m CPU request, which is plenty
# to push the HPA past its 70% target.

PIDFILE=/tmp/stress.pids

running() {
	[ -f "$PIDFILE" ] || return 1
	for pid in $(cat "$PIDFILE"); do
		kill -0 "$pid" 2>/dev/null && return 0
	done
	return 1
}

case "${1:-1}" in
stop)
	if [ -f "$PIDFILE" ]; then
		kill $(cat "$PIDFILE") 2>/dev/null
		rm -f "$PIDFILE"
		echo "stress stopped"
	else
		echo "stress is not running"
	fi
	;;
status)
	if running; then
		echo "stress running: $(wc -l <"$PIDFILE") worker(s), pids $(tr '\n' ' ' <"$PIDFILE")"
	else
		echo "stress is not running"
	fi
	;;
*[!0-9]* | 0)
	echo "usage: $0 [workers] | status | stop" >&2
	exit 1
	;;
*)
	if running; then
		echo "stress already running ($(wc -l <"$PIDFILE") worker(s)); run '$0 stop' first" >&2
		exit 1
	fi
	: >"$PIDFILE"
	i=0
	while [ "$i" -lt "$1" ]; do
		nohup /busybox/sh -c 'while :; do :; done' >/dev/null 2>&1 &
		echo $! >>"$PIDFILE"
		i=$((i + 1))
	done
	echo "stress started: $1 worker(s), each using one full CPU core"
	echo "watch:  kubectl -n sovereign-ai get hpa secure-intake -w"
	echo "stop:   /stress.sh stop   (or delete the pod)"
	;;
esac

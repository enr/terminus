#!/bin/sh
# terminus external module: is a reboot pending after package updates?
# Install: cp reboot.sh /etc/terminus/modules.d/ && chmod +x /etc/terminus/modules.d/reboot.sh
#
# Debian/Ubuntu write /var/run/reboot-required; Fedora/RHEL provide needs-restarting -r.

required=false
reason=""
if [ -f /var/run/reboot-required ]; then
  required=true
  reason="$(cat /var/run/reboot-required.pkgs 2>/dev/null | tr '\n' ' ')"
elif command -v needs-restarting >/dev/null 2>&1; then
  if ! needs-restarting -r >/dev/null 2>&1; then
    required=true
    reason="needs-restarting -r"
  fi
fi

if [ "$required" = true ]; then
  severity=warn
  message="a reboot is required to apply updates"
else
  severity=ok
  message="no reboot required"
fi

cat <<JSON
{
  "facts": {"reboot_required": $required, "packages": "$(echo "$reason" | sed 's/"/\\"/g; s/ *$//')"},
  "findings": [
    {"id": "reboot.required", "severity": "$severity", "subject": "kernel and libraries",
     "message": "$message", "hint": "plan a reboot: running services still use the old code"}
  ]
}
JSON

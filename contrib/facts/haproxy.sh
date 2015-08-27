#!/bin/bash

output=""

while read line; do
  IFS=, cols=($line)
  output="$output \"${cols[0]}\": {"
    output="$output \"session_current\": ${cols[4]},"
    output="$output \"session_total\": ${cols[7]},"

    output="$output \"bytes_in\": ${cols[8]},"
    output="$output \"bytes_out\": ${cols[9]},"

    output="$output \"connection_errors\": ${cols[13]},"

    output="$output \"warning_retries\": ${cols[15]},"
    output="$output \"warning_redispatched\": ${cols[16]},"

    if [[ -n ${cols[46]} ]]; then
      output="$output \"requests_per_second\": ${cols[46]},"
    fi

    if [[ -n ${cols[47]} ]]; then
      output="$output \"requests_per_second_max\": ${cols[47]},"
    fi

    output="$output \"queue_time\": ${cols[58]},"
    output="$output \"connect_time\": ${cols[59]},"
    output="$output \"response_time\": ${cols[60]},"
    output="$output \"average_time\": ${cols[61]}"

  output="$output },"
done < <(echo "show stat" | socat /var/lib/haproxy/stats stdio | grep BACKEND)

output="${output%?}"
echo "{ $output }"


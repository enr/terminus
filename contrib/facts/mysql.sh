#!/bin/bash

output=""

declare -A general_queries
general_queries["Bytes_received"]="1"
general_queries["Bytes_sent"]="1"
general_queries["Key_read_requests"]="1"
general_queries["Key_reads"]="1"
general_queries["Key_write_requests"]="1"
general_queries["Key_writes"]="1"
general_queries["Binlog_cache_use"]="1"
general_queries["Binlog_cache_disk_use"]="1"
general_queries["Max_used_connections"]="1"
general_queries["Aborted_clients"]="1"
general_queries["Aborted_connects"]="1"
general_queries["Threads_connected"]="1"
general_queries["Open_files"]="1"
general_queries["Open_tables"]="1"
general_queries["Opened_tables"]="1"
general_queries["Prepared_stmt_count"]="1"
general_queries["Seconds_Behind_Master"]="1"
general_queries["Select_full_join"]="1"
general_queries["Select_full_range_join"]="1"
general_queries["Select_range"]="1"
general_queries["Select_range_check"]="1"
general_queries["Select_scan"]="1"
general_queries["Slow_queries"]="1"
general_queries["Queries"]="1"

declare -A querycache
querycache["Qcache_queries_in_cache"]="1"
querycache["Qcache_hits"]="1"
querycache["Qcache_inserts"]="1"
querycache["Qcache_not_cached"]="1"
querycache["Qcache_lowmem_prunes"]="1"

declare -A commands
commands["Com_admin_commands"]="1"
commands["Com_begin"]="1"
commands["Com_change_db"]="1"
commands["Com_commit"]="1"
commands["Com_create_table"]="1"
commands["Com_drop_table"]="1"
commands["Com_show_keys"]="1"
commands["Com_delete"]="1"
commands["Com_create_db"]="1"
commands["Com_grant"]="1"
commands["Com_show_processlist"]="1"
commands["Com_flush"]="1"
commands["Com_insert"]="1"
commands["Com_purge"]="1"
commands["Com_replace"]="1"
commands["Com_rollback"]="1"
commands["Com_select"]="1"
commands["Com_set_option"]="1"
commands["Com_show_binlogs"]="1"
commands["Com_show_databases"]="1"
commands["Com_show_fields"]="1"
commands["Com_show_status"]="1"
commands["Com_show_tables"]="1"
commands["Com_show_variables"]="1"
commands["Com_update"]="1"
commands["Com_drop_db"]="1"
commands["Com_revoke"]="1"
commands["Com_drop_user"]="1"
commands["Com_show_grants"]="1"
commands["Com_lock_tables"]="1"
commands["Com_show_create_table"]="1"
commands["Com_unlock_tables"]="1"
commands["Com_alter_table"]="1"

declare -A counters
counters["Handler_write"]="1"
counters["Handler_update"]="1"
counters["Handler_delete"]="1"
counters["Handler_read_first"]="1"
counters["Handler_read_key"]="1"
counters["Handler_read_next"]="1"
counters["Handler_read_prev"]="1"
counters["Handler_read_rnd"]="1"
counters["Handler_read_rnd_next"]="1"
counters["Handler_commit"]="1"
counters["Handler_rollback"]="1"
counters["Handler_savepoint"]="1"
counters["Handler_savepoint_rollback"]="1"
counters["Max_prepared_stmt_count"]="1"

declare -A innodb
innodb["Innodb_buffer_pool_pages_total"]="1"
innodb["Innodb_buffer_pool_pages_free"]="1"
innodb["Innodb_buffer_pool_pages_dirty"]="1"
innodb["Innodb_buffer_pool_pages_data"]="1"
innodb["Innodb_page_size"]="1"
innodb["Innodb_pages_created"]="1"
innodb["Innodb_pages_read"]="1"
innodb["Innodb_pages_written"]="1"
innodb["Innodb_row_lock_current_waits"]="1"
innodb["Innodb_row_lock_waits"]="1"
innodb["Innodb_row_lock_time"]="1"
innodb["Innodb_data_reads"]="1"
innodb["Innodb_data_writes"]="1"
innodb["Innodb_data_fsyncs"]="1"
innodb["Innodb_log_writes"]="1"
innodb["Innodb_rows_updated"]="1"
innodb["Innodb_rows_read"]="1"
innodb["Innodb_rows_deleted"]="1"
innodb["Innodb_rows_inserted"]="1"

declare -A wsrep
wsrep["wsrep_replicated"]="1"
wsrep["wsrep_replicated_bytes"]="1"
wsrep["wsrep_repl_keys"]="1"
wsrep["wsrep_repl_keys_bytes"]="1"
wsrep["wsrep_repl_data_bytes"]="1"
wsrep["wsrep_repl_other_bytes"]="1"
wsrep["wsrep_received"]="1"
wsrep["wsrep_received_bytes"]="1"
wsrep["wsrep_local_commits"]="1"
wsrep["wsrep_local_send_queue"]="1"
wsrep["wsrep_local_send_queue_avg"]="1"
wsrep["wsrep_local_recv_queue"]="1"
wsrep["wsrep_local_recv_queue_avg"]="1"
wsrep["wsrep_local_cached_downto"]="1"

while read line; do
  l=($line)
  key="${l[0]}"
  value="${l[1]}"

  if [[ ${general_queries[$key]+isset} ]]; then
    output="$output \"$key\": $value,"
  fi

  if [[ ${querycache[$key]+isset} ]]; then
    output="$output \"$key\": $value,"
  fi

  if [[ ${commands[$key]+isset} ]]; then
    output="$output \"$key\": $value,"
  fi

  if [[ ${counters[$key]+isset} ]]; then
    output="$output \"$key\": $value,"
  fi

  if [[ ${innodb[$key]+isset} ]]; then
    output="$output \"$key\": $value,"
  fi

  if [[ ${wsrep[$key]+isset} ]]; then
    output="$output \"$key\": $value,"
  fi

done < <(mysql --defaults-extra-file=/etc/mysql/debian.cnf -B --disable-column-names -e "show global status")

output="${output::-1}"
echo "{$output}"

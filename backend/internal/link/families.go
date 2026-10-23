package link

import "sort"

// MetricFamily is one exported metric family: its name, and its type as the exposition format says it ("counter", "gauge" or
// "histogram"). A histogram is exported as the series name_bucket, name_sum and name_count.
type MetricFamily struct {
	Name, Type string
}

// families is every family the meter can export, whichever of its sources are on. TestMetricFamiliesAreWhatTheMeterExports
// renders a meter with every source active and fails when this list and the output differ, so it cannot drift.
var families = []MetricFamily{
	{"ikhnos_link_peer_info", "gauge"},
	{"ikhnos_link_connections_total", "counter"},
	{"ikhnos_link_sent_bytes_total", "counter"},
	{"ikhnos_link_received_bytes_total", "counter"},
	{"ikhnos_link_segments_sent_total", "counter"},
	{"ikhnos_link_retransmits_total", "counter"},
	{"ikhnos_link_rto_retransmits_total", "counter"},
	{"ikhnos_link_receive_drops_total", "counter"},
	{"ikhnos_link_connection_failures_total", "counter"},
	{"ikhnos_link_rtt_seconds", "histogram"},
	{"ikhnos_link_last_observation_timestamp_seconds", "gauge"},
	{"ikhnos_link_probe_packets_sent_total", "counter"},
	{"ikhnos_link_probe_packets_lost_total", "counter"},
	{"ikhnos_link_probe_packets_duplicated_total", "counter"},
	{"ikhnos_link_probe_failures_total", "counter"},
	{"ikhnos_link_probe_loss_ratio", "gauge"},
	{"ikhnos_link_probe_delay_variation_seconds", "gauge"},
	{"ikhnos_link_probe_one_way_delay_seconds", "gauge"},
	{"ikhnos_link_probe_clock_synchronized", "gauge"},
	{"ikhnos_link_probe_clock_error_seconds", "gauge"},
	{"ikhnos_link_probe_path_hops", "gauge"},
	{"ikhnos_link_probe_up", "gauge"},
	{"ikhnos_link_probe_last_success_timestamp_seconds", "gauge"},
	{"ikhnos_link_path_mtu_bytes", "gauge"},
	{"ikhnos_link_path_reached", "gauge"},
	{"ikhnos_link_path_mtu_blackhole", "gauge"},
	{"ikhnos_link_path_changes_total", "counter"},
	{"ikhnos_link_path_probe_failures_total", "counter"},
	{"ikhnos_link_path_last_success_timestamp_seconds", "gauge"},
	{"ikhnos_link_tcp_connect_seconds", "histogram"},
	{"ikhnos_link_tcp_connect_failures_total", "counter"},
	{"ikhnos_link_tcp_connect_last_success_timestamp_seconds", "gauge"},
	{"ikhnos_link_throughput_bits_per_second", "gauge"},
	{"ikhnos_link_throughput_bounded", "gauge"},
	{"ikhnos_link_throughput_retransmits", "gauge"},
	{"ikhnos_link_throughput_rtt_seconds", "gauge"},
	{"ikhnos_link_throughput_udp_point_bits_per_second", "gauge"},
	{"ikhnos_link_throughput_udp_loss_ratio", "gauge"},
	{"ikhnos_link_throughput_udp_jitter_seconds", "gauge"},
	{"ikhnos_link_throughput_udp_clean_bits_per_second", "gauge"},
	{"ikhnos_link_throughput_udp_saturated", "gauge"},
	{"ikhnos_link_throughput_test_bytes_total", "counter"},
	{"ikhnos_link_throughput_test_failures_total", "counter"},
	{"ikhnos_link_throughput_tests_skipped_total", "counter"},
	{"ikhnos_link_throughput_last_success_timestamp_seconds", "gauge"},
	{"ikhnos_link_throughput_pair_budget_used_bytes", "gauge"},
	{"ikhnos_link_throughput_pair_budget_bytes", "gauge"},
	{"ikhnos_link_throughput_budget_used_bytes", "gauge"},
	{"ikhnos_link_throughput_budget_bytes", "gauge"},
}

// MetricFamilies lists every metric family the link meter exports, sorted by name. Other packages that name these series
// (the FUSION dashboards, the queries of the Sites screen) check what they use against it.
func MetricFamilies() []MetricFamily {
	out := append([]MetricFamily(nil), families...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

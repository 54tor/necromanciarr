package i18n

// Clés des actions volumes list, volumes probe, volumes commands et de la commande sftp trust (CLI seulement).
const (
	KeyErrorVolumeNotFound     Key = "error.volume_not_found"
	KeyErrorVolumeProbeRunning Key = "error.volume_probe_running"

	KeyActionVolumesList     Key = "action.volumes.list"
	KeyActionVolumesProbe    Key = "action.volumes.probe"
	KeyActionVolumesCommands Key = "action.volumes.commands"

	KeyCLISummaryVolumesList     Key = "cli.summary.volumes_list"
	KeyCLISummaryVolumesProbe    Key = "cli.summary.volumes_probe"
	KeyCLISummaryVolumesCommands Key = "cli.summary.volumes_commands"
	KeyCLIUsageSFTPTrust         Key = "cli.usage.sftp_trust"

	KeyCLIHeaderVolume       Key = "cli.header.volume"
	KeyCLIHeaderAddress      Key = "cli.header.address"
	KeyCLIVolumeCapabilities Key = "cli.volumes.capabilities"
	KeyCLIHeaderProbedAt     Key = "cli.header.probed_at"
	KeyCLIVolumeLocal        Key = "cli.volumes.local"
	KeyCLIVolumeNotProbed    Key = "cli.volumes.not_probed"
	KeyCLIVolumeProbeOK      Key = "cli.volumes.probe_ok"
	KeyCLIVolumeProbeFailed  Key = "cli.volumes.probe_failed"
	KeyCLIVolumeCommandsHint Key = "cli.volumes.commands_hint"

	KeyCLISFTPTrustFingerprint     Key = "cli.sftp_trust.fingerprint"
	KeyCLISFTPTrustVerify          Key = "cli.sftp_trust.verify"
	KeyCLISFTPTrustNeedsYes        Key = "cli.sftp_trust.needs_yes"
	KeyCLISFTPTrustAdded           Key = "cli.sftp_trust.added"
	KeyCLISFTPTrustAlreadyPinned   Key = "cli.sftp_trust.already_pinned"
	KeyCLISFTPTrustUnknownProvider Key = "cli.sftp_trust.unknown_provider"
	KeyCLISFTPTrustNotSFTP         Key = "cli.sftp_trust.not_sftp"
	KeyCLISFTPTrustFailed          Key = "cli.sftp_trust.failed"

	KeyReasonNoLinkDir             Key = "reason.no_link_dir"
	KeyReasonKnownHostsNotWritable Key = "reason.known_hosts_not_writable"
)

var volumeKeys = []Key{
	KeyErrorVolumeNotFound, KeyErrorVolumeProbeRunning, KeyActionVolumesList, KeyActionVolumesProbe,
	KeyActionVolumesCommands, KeyCLISummaryVolumesList, KeyCLISummaryVolumesProbe, KeyCLISummaryVolumesCommands,
	KeyCLIUsageSFTPTrust, KeyCLIHeaderVolume, KeyCLIHeaderAddress, KeyCLIVolumeCapabilities, KeyCLIHeaderProbedAt,
	KeyCLIVolumeLocal, KeyCLIVolumeNotProbed, KeyCLIVolumeProbeOK, KeyCLIVolumeProbeFailed, KeyCLIVolumeCommandsHint,
	KeyCLISFTPTrustFingerprint, KeyCLISFTPTrustVerify, KeyCLISFTPTrustNeedsYes, KeyCLISFTPTrustAdded,
	KeyCLISFTPTrustAlreadyPinned, KeyCLISFTPTrustUnknownProvider, KeyCLISFTPTrustNotSFTP, KeyCLISFTPTrustFailed,
	KeyReasonNoLinkDir, KeyReasonKnownHostsNotWritable,
}

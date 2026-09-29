package term

// DetachKey 终端脱离键：Ctrl+]（0x1d，与 telnet 一致）。
// 它不会发送给远端；Ctrl+C（0x03）则会原样透传，用来中断远端进程。
const DetachKey byte = 0x1d

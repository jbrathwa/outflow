# Records docs/img/doctor.gif. Run from the repo root with the outflow
# binary on PATH, e.g.:
#   go build -o /tmp/outflow-bin/outflow .
#   PATH=/tmp/outflow-bin:$PATH vhs docs/doctor.tape
Output docs/img/doctor.gif

Set Shell "bash"
Set FontSize 15
Set Width 1400
Set Height 900
Set Padding 12
Set Framerate 10
Set TypingSpeed 40ms

Type "clear" Enter
Sleep 300ms
Type "outflow doctor --demo-degraded"
Sleep 600ms
Enter
Sleep 5000ms

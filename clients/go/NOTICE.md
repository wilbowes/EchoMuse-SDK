# Third-party components

EchoMuse SDK is MIT licensed (see `LICENSE`). It depends on the components
below, each of which keeps its own licence. All are permissive and compatible
with redistribution under MIT.

## Depended on

| Component | Where | Licence | Copyright |
|---|---|---|---|
| [gorilla/websocket](https://github.com/gorilla/websocket) | `go.mod` | BSD-2-Clause | The Gorilla Authors |
| [grandcat/zeroconf](https://github.com/grandcat/zeroconf) | `go.mod` | BSD-3-Clause | Frank Wang |
| [flynn/noise](https://github.com/flynn/noise) | `go.mod` | BSD-3-Clause | Prime Directive, Inc. |
| [mewkiz/flac](https://github.com/mewkiz/flac) | `go.mod` | Public domain | mewkiz |
| [ONNX Runtime C API](https://github.com/microsoft/onnxruntime) | `echomuse/wakeword/ort/include/onnxruntime_c_api.h` | MIT | Microsoft Corporation; see adjacent `COPYING` |

## Related projects

This SDK implements the device side of the EchoMuse controller's device
interface. The protocol specification, the controller and the reference
firmware live in the [EchoMuse](https://github.com/wilbowes/EchoMuse)
repository, which is MIT licensed.

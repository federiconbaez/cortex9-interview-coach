// AudioWorkletProcessor: copia cada bloque (128 muestras, canal 0) al hilo principal.
// Archivo JS plano porque se carga con audioWorklet.addModule().
class Cx9Capture extends AudioWorkletProcessor {
  process(inputs) {
    const ch = inputs[0] && inputs[0][0];
    if (ch) this.port.postMessage(ch.slice(0));
    return true;
  }
}
registerProcessor('cx9-capture', Cx9Capture);

/** Only connection failure calls this function. No mailbox queries run on timers. */
export function reconnectDelay(ms: number, signal: AbortSignal): Promise<void> {
  signal.throwIfAborted();
  return new Promise((resolve, reject) => {
    const abort = () => { clearTimeout(timer); reject(signal.reason); };
    const timer = setTimeout(() => { signal.removeEventListener('abort', abort); resolve(); }, ms); // allowed-timer: backoff after connection failure only
    signal.addEventListener('abort', abort, {once: true});
  });
}

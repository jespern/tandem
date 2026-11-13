import {Buffer} from 'buffer';
import {unb32, randomBytes} from '../crypto.js';

export function validEnvelope(body: Uint8Array): boolean {
  return body.length >= 54 && body[0] === 84 && body[1] === 78 && body[2] === 68 && body[3] === 77 && body[4] === 2 &&
    (body[5] === 1 || body[5] === 3 || (body[5] === 2 && body.length >= 86));
}
export function checkChannel(channel: string): void { if (unb32(channel).length !== 20) throw new Error('invalid channel'); }
export function buildMessage(channel: string, body: Uint8Array): Uint8Array {
  checkChannel(channel); if (!validEnvelope(body)) throw new Error('invalid v2 envelope');
  const encoded = Buffer.from(body).toString('base64').match(/.{1,76}/g)!.join('\r\n');
  return new TextEncoder().encode(['From: Tandem <tandem@invalid>', `Date: ${new Date().toUTCString()}`,
    `Message-ID: <${randomBytes(16).toString('hex')}@tandem.invalid>`, 'Subject: tandem', 'MIME-Version: 1.0',
    'Tandem-Version: 2', `Tandem-Channel: ${channel}`, 'Content-Type: text/plain; charset=us-ascii',
    'Content-Transfer-Encoding: 7bit', '', encoded, ''].join('\r\n'));
}
export function decodeBody(text: string): Uint8Array {
  const compact = text.replace(/[\t\n\v\f\r ]/g, '');
  if (!/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(compact)) throw new Error('invalid MIME base64');
  const decoded = Buffer.from(compact, 'base64');
  if (decoded.toString('base64') !== compact || !validEnvelope(decoded)) throw new Error('invalid v2 body');
  return Uint8Array.from(decoded);
}

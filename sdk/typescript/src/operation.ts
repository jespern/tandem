export type ExportGuard = {type:'control'} | {type:'home';epoch:string} | {type:'collection';collection:string;epoch:string} | {type:'recipient';certificate:string;collection?:string;epoch?:string} | {type:'action';id:string};
export interface OutgoingMessage {channel:string;body:Uint8Array;guard?:ExportGuard;deferred?:boolean}
export interface OperationIntent {type:string;values:Record<string,string>}
export interface OperationPhase {intent?:OperationIntent;requires?:number[];destroyQueued?:boolean;name:string;messages:OutgoingMessage[];destroy?:string[];cancel?:string[]}
export interface PreparedOperation {kind:string;details:Record<string,string>;phases:OperationPhase[]}
export interface MailboxPresence {id:string;channel:string;hash:string}

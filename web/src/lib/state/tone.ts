/**
 * 语义色档：全应用共用一套（否则会出现"漂移的 warn"与"目标的 warn"不同名，
 * 组件之间无法互相传递）。`unknown` 一律使用 warn，绝不与 ok 共用外观（M5）。
 */
export type Tone = 'ok' | 'warn' | 'danger' | 'muted' | 'info';

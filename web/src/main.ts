import { mount } from 'svelte';
import App from './App.svelte';
import { initLocale } from './lib/i18n/index.js';
import './app.css';

// 先恢复语言偏好再挂载，保证首帧就是所选语言。
initLocale();

const target = document.getElementById('app');
if (!target) throw new Error('#app container not found');

const app = mount(App, { target });

export default app;

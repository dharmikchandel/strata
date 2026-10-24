// The benchmarks page, served at /benchmarks/.
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '../styles/global';
import { BenchApp } from './BenchApp';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BenchApp />
  </StrictMode>,
);

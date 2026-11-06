// Visor 3D de la ficha de un archivo.
//
// three.js 0.170.0 va incluido en static/three/. Sus complementos importaban
// 'three'; se reescribieron a rutas relativas para no necesitar un import map,
// que sería un script en línea y la CSP no lo permite.
import * as THREE from './three/three.module.min.js';
import { OrbitControls } from './three/addons/controls/OrbitControls.js';
import { GLTFLoader } from './three/addons/loaders/GLTFLoader.js';
import { FBXLoader } from './three/addons/loaders/FBXLoader.js';
import { OBJLoader } from './three/addons/loaders/OBJLoader.js';
import { STLLoader } from './three/addons/loaders/STLLoader.js';

// Imagen vacía de 1 × 1 que sustituye a las texturas en archivos aparte.
const EMPTY_PNG = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=';

const root = document.getElementById('viewer');
if (root) {
  const large = root.querySelector('[data-large]');
  if (large) {
    root.querySelector('[data-load]').addEventListener('click', () => {
      large.hidden = true;
      start(root);
    });
  } else {
    start(root);
  }
}

async function start(root) {
  const note = root.querySelector('[data-note]');
  const fit = root.querySelector('[data-fit]');
  const canvas = root.querySelector('canvas');
  note.textContent = 'Cargando el modelo…';

  // Un modelo nunca pide archivos al servidor: lo que esté fuera del archivo
  // subido se sustituye y se avisa.
  const missing = { count: 0 };
  const manager = new THREE.LoadingManager();
  manager.setURLModifier((url) => {
    if (url.startsWith('data:') || url.startsWith('blob:')) return url;
    missing.count++;
    return EMPTY_PNG;
  });

  let model;
  try {
    const response = await fetch(root.dataset.src);
    if (!response.ok) throw new Error('el servidor respondió ' + response.status);
    const data = await response.arrayBuffer();
    model = await parse(root.dataset.ext, data, manager, missing);
  } catch (err) {
    note.textContent = 'No se pudo mostrar el modelo: ' + (err && err.message ? err.message : err);
    return;
  }

  const renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true });
  renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
  const scene = new THREE.Scene();
  const camera = new THREE.PerspectiveCamera(45, 1, 0.01, 1000);
  scene.add(new THREE.HemisphereLight(0xffffff, 0x8a8f99, 1.2));
  const sun = new THREE.DirectionalLight(0xffffff, 1.2);
  sun.position.set(1, 2, 3);
  camera.add(sun);
  // Las dos caras: muchos modelos tienen normales invertidas o mallas abiertas.
  model.traverse((o) => {
    for (const m of [].concat(o.material || [])) m.side = THREE.DoubleSide;
  });
  scene.add(camera, model);

  const controls = new OrbitControls(camera, canvas);
  const render = () => renderer.render(scene, camera);
  controls.addEventListener('change', render);

  const resize = () => {
    const { clientWidth: w, clientHeight: h } = canvas;
    if (!w || !h) return;
    renderer.setSize(w, h, false);
    camera.aspect = w / h;
    camera.updateProjectionMatrix();
    render();
  };
  new ResizeObserver(resize).observe(canvas);

  const box = new THREE.Box3().setFromObject(model);
  if (box.isEmpty()) {
    note.textContent = 'El modelo no tiene geometría visible.';
    return;
  }
  const frame = () => {
    const size = box.getSize(new THREE.Vector3()).length();
    const center = box.getCenter(new THREE.Vector3());
    const distance = 1.2 * size / (2 * Math.tan(THREE.MathUtils.degToRad(camera.fov) / 2));
    camera.near = distance / 100;
    camera.far = distance * 100;
    camera.position.copy(center).add(new THREE.Vector3(1, 0.7, 1).normalize().multiplyScalar(distance));
    camera.updateProjectionMatrix();
    controls.target.copy(center);
    controls.update();
    render();
  };
  fit.addEventListener('click', frame);
  fit.hidden = false;
  resize();
  frame();

  note.textContent = missing.count > 0
    ? 'Este modelo usa archivos aparte (texturas o materiales) que no se muestran: solo se ve la geometría.'
    : '';
}

async function parse(ext, data, manager, missing) {
  switch (ext) {
    case '.glb':
    case '.gltf': {
      if (ext === '.gltf') {
        const json = JSON.parse(new TextDecoder().decode(data));
        const outside = (json.buffers || []).some((b) => b.uri && !b.uri.startsWith('data:'));
        if (outside) throw new Error('la geometría está en un archivo .bin aparte.');
      }
      const gltf = await new GLTFLoader(manager).parseAsync(data, '');
      return gltf.scene;
    }
    case '.fbx':
      return new FBXLoader(manager).parse(data, '');
    case '.obj': {
      const text = new TextDecoder().decode(data);
      const group = new OBJLoader(manager).parse(text);
      // OBJLoader no lee los materiales: un mtllib es un archivo aparte.
      if (/^\s*mtllib\s/m.test(text)) missing.count++;
      return group;
    }
    case '.stl': {
      const geometry = new STLLoader().parse(data);
      const material = new THREE.MeshStandardMaterial({ color: 0xb8bcc4, vertexColors: geometry.hasColors === true });
      return new THREE.Mesh(geometry, material);
    }
  }
  throw new Error('formato no admitido.');
}

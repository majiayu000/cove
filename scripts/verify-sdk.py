"""Official Python SDK acceptance against an isolated Cove binary and TCP fixtures.

Create a temporary venv, install verify-sdk-requirements.txt, then run:
  python scripts/verify-sdk.py --binary /absolute/path/to/gatt --output /tmp/sdk.json
Only synthetic loopback credentials/provider events are used. No tools execute.
"""

import argparse
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.request import Request, build_opener, ProxyHandler

import anthropic
from google import genai
from google.genai import errors as genai_errors, types
import httpx
import httpx2
import openai


ROOT = Path(__file__).resolve().parent.parent
ADMIN_HTTP = build_opener(ProxyHandler({}))
MODEL = 'fixture-model'
TEXT = 'sdk synthetic 你好'
SOURCE_SECRET = 'sdk-synthetic-upstream-secret'
CALL_ID = 'sdk-function-call'
FUNCTION = {'name': 'lookup', 'description': 'synthetic fixture', 'parameters': {
    'type': 'object', 'properties': {'x': {'type': 'integer'}}, 'required': ['x'],
}}


def check(condition, message):
    if not condition:
        raise AssertionError(message)


def encode(value):
    return json.dumps(value, ensure_ascii=False, separators=(',', ':')).encode()


def port_available():
    # An isolated ephemeral listener, never a production service's fixed port.
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def admin(base, path, method='GET', body=None, session=None):
    headers = {'Origin': base, 'Content-Type': 'application/json'}
    if session:
        headers['Authorization'] = 'Bearer ' + session
    request = Request(base + path, encode(body) if body is not None else None,
                      headers, method=method)
    try:
        response = ADMIN_HTTP.open(request, timeout=8)
    except HTTPError as error:
        response = error
    with response:
        return response.status, json.loads(response.read())


class ProviderState:
    def __init__(self):
        self.lock = threading.Lock()
        self.mode = 'text'
        self.native = ''
        self.calls = []
        self.errors = []
        self.cancelled = threading.Event()

    def select(self, native, mode):
        with self.lock:
            self.native, self.mode = native, mode
            self.calls = []
            self.errors = []
            self.cancelled.clear()


def provider_protocol(path):
    if ':generateContent' in path or ':streamGenerateContent' in path:
        return 'gemini'
    if path.endswith('/chat/completions'):
        return 'chat_completions'
    if path.endswith('/messages'):
        return 'messages'
    if path.endswith('/responses'):
        return 'responses'
    raise AssertionError('unexpected synthetic native endpoint: ' + path)


def assert_tool_result(native, body):
    if native == 'gemini':
        results = [p['functionResponse'] for c in body['contents'] for p in c['parts']
                   if 'functionResponse' in p]
        check(len(results) == 1 and results[0]['name'] == 'lookup', 'Gemini tool result lost')
        check(results[0].get('id') == CALL_ID, 'Gemini tool call identity changed')
        check(results[0]['response'] == {'value': 2}, 'Gemini tool result changed')
    elif native == 'responses':
        results = [i for i in body['input'] if i.get('type') == 'function_call_output']
        check(len(results) == 1 and results[0]['call_id'] == CALL_ID, 'Responses tool owner changed')
        check(json.loads(results[0]['output']) == {'value': 2}, 'Responses tool result changed')
    elif native == 'chat_completions':
        results = [i for i in body['messages'] if i.get('role') == 'tool']
        check(len(results) == 1 and results[0]['tool_call_id'] == CALL_ID, 'Chat tool owner changed')
        check(json.loads(results[0]['content']) == {'value': 2}, 'Chat tool result changed')
    else:
        results = [p for m in body['messages'] for p in m.get('content', [])
                   if isinstance(p, dict) and p.get('type') == 'tool_result']
        check(len(results) == 1 and results[0]['tool_use_id'] == CALL_ID, 'Messages tool owner changed')
        content = results[0]['content']
        if isinstance(content, list):
            content = ''.join(p['text'] for p in content if p.get('type') == 'text')
        check(json.loads(content) == {'value': 2}, 'Messages tool result changed')


def native_response(native, response_id, tool=False):
    if native == 'gemini':
        part = {'functionCall': {'id': CALL_ID, 'name': 'lookup', 'args': {'x': 1}}} if tool else {'text': TEXT}
        return {'responseId': response_id, 'modelVersion': MODEL, 'candidates': [
            {'index': 0, 'content': {'role': 'model', 'parts': [part]}, 'finishReason': 'STOP'}],
            'usageMetadata': {'promptTokenCount': 7, 'candidatesTokenCount': 3,
                              'thoughtsTokenCount': 0, 'totalTokenCount': 10}}
    if native == 'responses':
        item = {'id': 'fc-' + response_id, 'type': 'function_call', 'status': 'completed',
                'call_id': CALL_ID, 'name': 'lookup', 'arguments': '{"x":1}'} if tool else {
                    'id': 'msg-' + response_id, 'type': 'message', 'role': 'assistant',
                    'status': 'completed', 'content': [{'type': 'output_text', 'text': TEXT, 'annotations': []}]}
        return {'id': response_id, 'object': 'response', 'created_at': 1790726400,
                'status': 'completed', 'model': MODEL, 'output': [item],
                'usage': {'input_tokens': 7, 'output_tokens': 3, 'total_tokens': 10,
                          'output_tokens_details': {'reasoning_tokens': 0}}}
    if native == 'chat_completions':
        message = {'role': 'assistant', 'content': None, 'tool_calls': [
            {'id': CALL_ID, 'type': 'function', 'function': {'name': 'lookup', 'arguments': '{"x":1}'}}]} if tool else {
                'role': 'assistant', 'content': TEXT}
        return {'id': response_id, 'object': 'chat.completion', 'created': 1790726400,
                'model': MODEL, 'choices': [{'index': 0, 'message': message,
                                            'finish_reason': 'tool_calls' if tool else 'stop'}],
                'usage': {'prompt_tokens': 7, 'completion_tokens': 3, 'total_tokens': 10,
                          'completion_tokens_details': {'reasoning_tokens': 0}}}
    return {'id': response_id, 'type': 'message', 'role': 'assistant', 'model': MODEL,
            'content': [{'type': 'tool_use', 'id': CALL_ID, 'name': 'lookup', 'input': {'x': 1}}] if tool else [
                {'type': 'text', 'text': TEXT}], 'stop_reason': 'tool_use' if tool else 'end_turn',
            'stop_sequence': None, 'usage': {'input_tokens': 7, 'output_tokens': 3}}


def stream_events(native, response):
    response_id = response.get('id', response.get('responseId'))
    if native == 'gemini':
        first = {'responseId': response_id, 'modelVersion': MODEL, 'candidates': [
            {'index': 0, 'content': {'role': 'model', 'parts': [{'text': 'sdk'}]}}]}
        last = dict(response)
        last['candidates'] = [{'index': 0, 'content': {'role': 'model', 'parts': [
            {'text': ' synthetic 你好'}]}, 'finishReason': 'STOP'}]
        return [(None, first), (None, last)]
    if native == 'chat_completions':
        def chunk(delta, reason=None):
            return {'id': response_id, 'object': 'chat.completion.chunk', 'created': 1790726400,
                    'model': MODEL, 'choices': [{'index': 0, 'delta': delta, 'finish_reason': reason}]}
        return [(None, chunk({'role': 'assistant', 'content': ''})),
                (None, chunk({'content': 'sdk'})), (None, chunk({'content': ' synthetic 你好'})),
                (None, chunk({}, 'stop')), (None, {'id': response_id, 'object': 'chat.completion.chunk',
                                                 'created': 1790726400, 'model': MODEL, 'choices': [],
                                                 'usage': response['usage']}), (None, '[DONE]')]
    if native == 'messages':
        start = dict(response, content=[], stop_reason=None, usage={'input_tokens': 7, 'output_tokens': 0})
        return [('message_start', {'type': 'message_start', 'message': start}),
                ('content_block_start', {'type': 'content_block_start', 'index': 0,
                                         'content_block': {'type': 'text', 'text': ''}}),
                ('content_block_delta', {'type': 'content_block_delta', 'index': 0,
                                         'delta': {'type': 'text_delta', 'text': 'sdk'}}),
                ('content_block_delta', {'type': 'content_block_delta', 'index': 0,
                                         'delta': {'type': 'text_delta', 'text': ' synthetic 你好'}}),
                ('content_block_stop', {'type': 'content_block_stop', 'index': 0}),
                ('message_delta', {'type': 'message_delta', 'delta': {'stop_reason': 'end_turn',
                                                                    'stop_sequence': None},
                                   'usage': {'output_tokens': 3}}), ('message_stop', {'type': 'message_stop'})]
    item = response['output'][0]
    part = item['content'][0]
    def event(kind, **fields):
        return kind, dict(type=kind, **fields)
    return [event('response.created', response=dict(response, status='in_progress', output=[], usage=None)),
            event('response.output_item.added', output_index=0, item=dict(item, status='in_progress', content=[])),
            event('response.content_part.added', item_id=item['id'], output_index=0, content_index=0,
                  part=dict(part, text='')),
            event('response.output_text.delta', item_id=item['id'], output_index=0, content_index=0, delta='sdk'),
            event('response.output_text.delta', item_id=item['id'], output_index=0, content_index=0,
                  delta=' synthetic 你好'),
            event('response.output_text.done', item_id=item['id'], output_index=0, content_index=0, text=TEXT),
            event('response.content_part.done', item_id=item['id'], output_index=0, content_index=0, part=part),
            event('response.output_item.done', output_index=0, item=item),
            event('response.completed', response=response)]


def native_text_delta(native, value):
    if not isinstance(value, dict):
        return False
    if native == 'gemini':
        return any(p.get('text') for c in value.get('candidates', []) for p in c['content']['parts'])
    if native == 'chat_completions':
        return any(c.get('delta', {}).get('content') for c in value.get('choices', []))
    if native == 'messages':
        return value.get('type') == 'content_block_delta' and bool(value['delta'].get('text'))
    return value.get('type') == 'response.output_text.delta' and bool(value.get('delta'))


class SyntheticProvider(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, *_):
        pass

    def do_POST(self):
        state = self.server.state
        try:
            body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            native = provider_protocol(self.path)
            credentials = [self.headers.get('Authorization') == 'Bearer ' + SOURCE_SECRET,
                           self.headers.get('X-Api-Key') == SOURCE_SECRET,
                           self.headers.get('X-Goog-Api-Key') == SOURCE_SECRET]
            check(sum(credentials) == 1, 'client/upstream credential isolation failed')
            with state.lock:
                check(native == state.native, 'wrong native provider selected')
                index = len(state.calls)
                state.calls.append({'native': native, 'path': self.path, 'stream': bool(
                    body.get('stream') or ':streamGenerateContent' in self.path)})
                mode = state.mode
            if mode == 'tool' and index == 1:
                assert_tool_result(native, body)
            response = native_response(native, 'sdk-' + str(time.monotonic_ns()), mode == 'tool' and index == 0)
            if not body.get('stream') and ':streamGenerateContent' not in self.path:
                raw = encode(response)
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)
                return
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.send_header('Connection', 'close')
            self.end_headers()
            events = stream_events(native, response)
            for event_name, value in events:
                raw = value.encode() if isinstance(value, str) else encode(value)
                prefix = ('event: ' + event_name + '\n').encode() if event_name else b''
                self.wfile.write(prefix + b'data: ' + raw + b'\n\n')
                self.wfile.flush()
                if mode == 'cancel' and native_text_delta(native, value):
                    while True:
                        self.wfile.write(b': synthetic heartbeat\n\n')
                        self.wfile.flush()
                        time.sleep(.03)
            self.close_connection = True
        except (BrokenPipeError, ConnectionResetError):
            state.cancelled.set()
        except Exception as error:
            with state.lock:
                state.errors.append(str(error))
            raw = encode({'error': {'message': str(error)}})
            try:
                self.send_response(500)
                self.send_header('Content-Length', str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)
            except (BrokenPipeError, ConnectionResetError):
                pass
        finally:
            self.close_connection = True


class SDKClient:
    def __init__(self, protocol, base, key):
        self.protocol = protocol
        check(base.startswith('http://127.0.0.1:'), 'SDK endpoint must be synthetic loopback')
        if protocol in ('responses', 'chat_completions'):
            self.client = openai.OpenAI(api_key=key, base_url=base + '/v1', max_retries=0,
                                        http_client=httpx2.Client(trust_env=False, timeout=8))
        elif protocol == 'messages':
            self.client = anthropic.Anthropic(api_key=key, base_url=base, max_retries=0,
                                              http_client=httpx2.Client(trust_env=False, timeout=8))
        else:
            self.client = genai.Client(api_key=key, vertexai=False, http_options=types.HttpOptions(
                base_url=base, api_version='v1beta', httpx_client=httpx.Client(trust_env=False, timeout=8),
                retry_options=types.HttpRetryOptions(attempts=1)))

    def close(self):
        self.client.close()

    def create(self, history=None, stream=False, tool=False, unsupported=False):
        protocol = self.protocol
        if protocol == 'responses':
            kwargs = {'model': MODEL, 'input': history or 'synthetic question', 'max_output_tokens': 32}
            if tool:
                kwargs['tools'] = [dict(type='function', **FUNCTION)]
            if stream:
                kwargs['stream'] = True
            if unsupported:
                kwargs['extra_body'] = {'reasoning': {'effort': 'high'}}
            return self.client.responses.create(**kwargs)
        if protocol == 'chat_completions':
            kwargs = {'model': MODEL, 'messages': history or [{'role': 'user', 'content': 'synthetic question'}],
                      'max_tokens': 32}
            if tool:
                kwargs['tools'] = [{'type': 'function', 'function': FUNCTION}]
            if stream:
                kwargs.update(stream=True, stream_options={'include_usage': True})
            if unsupported:
                kwargs['logprobs'] = True
            return self.client.chat.completions.create(**kwargs)
        if protocol == 'messages':
            kwargs = {'model': MODEL, 'messages': history or [{'role': 'user', 'content': 'synthetic question'}],
                      'max_tokens': 32}
            if tool:
                kwargs['tools'] = [{'name': 'lookup', 'description': FUNCTION['description'],
                                    'input_schema': FUNCTION['parameters']}]
            if stream:
                kwargs['stream'] = True
            if unsupported:
                kwargs['thinking'] = {'type': 'enabled', 'budget_tokens': 16}
            return self.client.messages.create(**kwargs)
        config = {'max_output_tokens': 32, 'automatic_function_calling': {'disable': True}}
        if tool:
            config['tools'] = [{'function_declarations': [FUNCTION]}]
        if unsupported:
            config['safety_settings'] = [{'category': 'HARM_CATEGORY_HATE_SPEECH', 'threshold': 'BLOCK_NONE'}]
        method = self.client.models.generate_content_stream if stream else self.client.models.generate_content
        return method(model=MODEL, contents=history or 'synthetic question', config=config)

    def text(self, response):
        if self.protocol == 'responses':
            return response.output_text
        if self.protocol == 'chat_completions':
            return response.choices[0].message.content
        if self.protocol == 'messages':
            return ''.join(p.text for p in response.content if p.type == 'text')
        return response.text

    def usage(self, response):
        if self.protocol == 'chat_completions':
            return response.usage.prompt_tokens, response.usage.completion_tokens
        if self.protocol == 'gemini':
            u = response.usage_metadata
            # Messages supplies total output without candidate/reasoning split.
            # Nullable SDK fields must preserve that uncertainty; total minus
            # input remains an observed total output, never guessed candidates.
            if u.candidates_token_count is None:
                check(u.thoughts_token_count is None, 'candidate/reasoning uncertainty changed')
                return u.prompt_token_count, u.total_token_count - u.prompt_token_count
            return u.prompt_token_count, u.candidates_token_count + (u.thoughts_token_count or 0)
        return response.usage.input_tokens, response.usage.output_tokens

    def usage_evidence(self, response):
        if self.protocol == 'gemini':
            u = response.usage_metadata
            return {'input': u.prompt_token_count, 'candidates': u.candidates_token_count,
                    'reasoning': u.thoughts_token_count, 'total': u.total_token_count}
        return {'input': self.usage(response)[0], 'output': self.usage(response)[1]}

    def tool_history(self, response):
        if self.protocol == 'responses':
            call = next(p for p in response.output if p.type == 'function_call')
            check(call.call_id == CALL_ID and json.loads(call.arguments) == {'x': 1}, 'Responses typed tool changed')
            return [{'role': 'user', 'content': 'synthetic question'}, call.model_dump(exclude_none=True),
                    {'type': 'function_call_output', 'call_id': call.call_id, 'output': '{"value":2}'}]
        if self.protocol == 'chat_completions':
            message = response.choices[0].message
            call = message.tool_calls[0]
            check(call.id == CALL_ID and json.loads(call.function.arguments) == {'x': 1}, 'Chat typed tool changed')
            return [{'role': 'user', 'content': 'synthetic question'}, message.model_dump(exclude_none=True),
                    {'role': 'tool', 'tool_call_id': call.id, 'content': '{"value":2}'}]
        if self.protocol == 'messages':
            call = next(p for p in response.content if p.type == 'tool_use')
            check(call.id == CALL_ID and call.input == {'x': 1}, 'Messages typed tool changed')
            return [{'role': 'user', 'content': 'synthetic question'}, {'role': 'assistant',
                     'content': [p.model_dump(exclude_none=True) for p in response.content]},
                    {'role': 'user', 'content': [{'type': 'tool_result', 'tool_use_id': call.id,
                                                'content': '{"value":2}'}]}]
        call = response.function_calls[0]
        check(call.id == CALL_ID and call.args == {'x': 1}, 'Gemini typed tool changed')
        return [types.Content(role='user', parts=[types.Part(text='synthetic question')]),
                response.candidates[0].content, types.Content(role='user', parts=[types.Part(
                    function_response=types.FunctionResponse(id=call.id, name=call.name, response={'value': 2}))])]

    def stream_text(self, cancel=False):
        stream = self.create(stream=True)
        fragments, usage, observed_usage = [], None, None
        wire_source = stream.response.headers.get('X-Cove-Usage-Source') if self.protocol == 'messages' else None
        try:
            for event in stream:
                text = ''
                if self.protocol == 'responses':
                    if event.type == 'response.output_text.delta':
                        text = event.delta
                    if event.type == 'response.completed':
                        usage = self.usage(event.response)
                elif self.protocol == 'chat_completions':
                    if event.choices:
                        text = event.choices[0].delta.content or ''
                    if event.usage is not None:
                        usage = (event.usage.prompt_tokens, event.usage.completion_tokens)
                elif self.protocol == 'messages':
                    if event.type == 'content_block_delta' and event.delta.type == 'text_delta':
                        text = event.delta.text
                    if event.type == 'message_start':
                        usage = (event.message.usage.input_tokens, None)
                    if event.type == 'message_delta':
                        observed_input = event.usage.input_tokens
                        usage = (observed_input if observed_input is not None else usage[0], event.usage.output_tokens)
                else:
                    text = event.text or ''
                    if event.usage_metadata is not None:
                        usage = self.usage(event)
                        observed_usage = self.usage_evidence(event)
                if text:
                    fragments.append(text)
                    if cancel:
                        break
        finally:
            stream.close()
        if not cancel:
            check(''.join(fragments) == TEXT, 'typed SSE text changed: ' + repr(fragments))
            if self.protocol == 'messages':
                # Spec 9.3/34.1 keeps one estimated tokenizer basis throughout
                # converted Messages wire; actual usage belongs to the ledger.
                check(wire_source and wire_source.startswith('estimated;o200k_base;'),
                      'converted Messages SDK stream did not disclose wire estimate')
                check(all(isinstance(v, int) and v >= 0 for v in usage), 'estimated wire schema is not numeric')
            else:
                check(usage == (7, 3), 'typed SSE actual usage lost: ' + repr(usage))
        else:
            check(bool(fragments), 'cancellation did not consume native content')
        return {'text_parts': len(fragments), 'wire_usage': usage, 'wire_usage_source': wire_source,
                'sdk_usage_fields': observed_usage}


def wait_records(base, session, key_id, after, ended):
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        status, response = admin(base, '/admin/requests?limit=200', session=session)
        check(status == 200, 'request record listing failed')
        records = [r for r in response['items'] if r.get('client_key_id') == key_id and r['id'] not in after]
        if len(records) >= ended and all(r.get('ended_at') for r in records):
            return records
        time.sleep(.03)
    raise AssertionError('SDK records did not finish: ' + repr([
        {'id': r['id'], 'status': r['status'], 'ended': r.get('ended_at')} for r in records]))


def run_case(report, state, sdk, native, mode, base, session, key_id):
    status, existing = admin(base, '/admin/requests?limit=200', session=session)
    check(status == 200, 'baseline record query failed')
    after = {r['id'] for r in existing['items']}
    case = {'direction': sdk.protocol + '->' + native, 'case': mode, 'passed': False}
    state.select(native, mode)
    try:
        expected = 1
        if mode == 'text':
            response = sdk.create()
            check(sdk.text(response) == TEXT, 'typed JSON text changed')
            check(sdk.usage(response) == (7, 3), 'typed JSON actual usage lost: ' + repr(sdk.usage(response)))
            case['sdk_usage_fields'] = sdk.usage_evidence(response)
        elif mode == 'stream':
            case.update(sdk.stream_text())
        elif mode == 'tool':
            first = sdk.create(tool=True)
            check(sdk.usage(first) == (7, 3), 'first tool usage lost')
            response = sdk.create(history=sdk.tool_history(first), tool=True)
            check(sdk.text(response) == TEXT, 'tool result continuation text lost')
            check(sdk.usage(response) == (7, 3), 'continuation usage lost')
            case['sdk_usage_fields'] = sdk.usage_evidence(response)
            expected = 2
        elif mode == 'cancel':
            case.update(sdk.stream_text(cancel=True))
            check(state.cancelled.wait(3), 'SDK close did not cancel native upstream TCP read/write')
        elif mode == 'unsupported':
            expected = 0
            try:
                sdk.create(unsupported=True)
                raise AssertionError('unsupported cross-protocol field silently accepted')
            except (openai.APIStatusError, anthropic.APIStatusError, genai_errors.APIError) as error:
                code = getattr(error, 'status_code', getattr(error, 'code', None))
                check(code == 422, 'SDK typed unsupported error changed status: ' + str(code))
                case['sdk_error_type'] = type(error).__name__
                case['http_status'] = code
        with state.lock:
            check(not state.errors, 'provider assertion failed: ' + repr(state.errors))
            check(len(state.calls) == expected, 'native dispatch count differs: ' + str(len(state.calls)))
            case['native_calls'] = len(state.calls)
        if expected:
            records = wait_records(base, session, key_id, after, expected)
            case['requests'] = [{'id': r['id'], 'status': r['status'], 'submission': r.get('submission_evidence'),
                                 'usage': r.get('usage'), 'delivery_status': r.get('delivery_status'),
                                 'wire_usage_source': r.get('wire_usage_source')}
                                for r in records]
            if mode == 'cancel':
                check(all(r['status'] in ('interrupted', 'cancelled', 'failed') for r in records),
                      'cancelled SDK stream fabricated successful terminal')
                check(all(r.get('estimated_cost') is None for r in records), 'cancelled unknown usage fabricated cost')
            else:
                check(all(r['status'] == 'succeeded' for r in records), 'SDK generation ledger did not succeed')
                check(all(r.get('usage', {}).get('input_tokens') == 7 and
                          r.get('usage', {}).get('output_tokens') == 3 for r in records),
                      'native observed ledger usage changed')
        case['passed'] = True
    except Exception as error:
        case['error_type'] = type(error).__name__
        case['error'] = str(error)[:1500]
        with state.lock:
            case['native_calls'] = len(state.calls)
            case['provider_errors'] = list(state.errors)
    report['cases'].append(case)
    print(json.dumps({k: case[k] for k in ('direction', 'case', 'passed', 'native_calls')}, ensure_ascii=False), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    report = {'scope': 'official Python SDKs, isolated Cove binary, synthetic loopback TCP providers; no real account/provider/tool execution',
              'sdk_versions': {name: importlib.metadata.version(name) for name in ('openai', 'anthropic', 'google-genai')},
              'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(), 'cases': []}
    proc, server = None, None
    with tempfile.TemporaryDirectory(prefix='cove-sdk-acceptance-') as temp_name:
        temp = Path(temp_name)
        private_home = temp / 'home'
        private_home.mkdir(mode=0o700)
        launchers = temp / 'launchers'
        launchers.mkdir()
        (launchers / 'open').write_text('#!/bin/sh\nexit 0\n')
        (launchers / 'open').chmod(0o700)
        # Keep SDK environment/profile discovery away from the user's credentials.
        clean_env = {'PATH': str(launchers) + os.pathsep + os.defpath,
                     'HOME': str(private_home), 'USERPROFILE': str(private_home),
                     'XDG_CONFIG_HOME': str(private_home / '.config'), 'LANG': 'en_US.UTF-8'}
        os.environ.clear()
        os.environ.update(clean_env)
        state = ProviderState()
        server = ThreadingHTTPServer(('127.0.0.1', 0), SyntheticProvider)
        server.state = state
        server.daemon_threads = True
        threading.Thread(target=server.serve_forever, daemon=True).start()
        provider_base = 'http://127.0.0.1:' + str(server.server_port)
        config = json.loads((ROOT / 'config.example.json').read_text())
        config.update(listen='127.0.0.1:' + str(port_available()), data_dir=str(temp / 'data'),
                      header_timeout_seconds=5, idle_timeout_seconds=8, total_timeout_seconds=20)
        config['codex'].update(base_url=provider_base, auth_base_url=provider_base)
        config_path = temp / 'config.json'
        config_path.write_bytes(encode(config))
        config_path.chmod(0o600)
        base = 'http://' + config['listen']
        sdk_clients = []
        try:
            proc = subprocess.Popen([str(binary), '-config', str(config_path), 'serve'], env=clean_env,
                                    cwd=temp, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                check(proc.poll() is None, 'isolated binary exited during startup')
                try:
                    if admin(base, '/healthz')[0] == 200:
                        break
                except (URLError, TimeoutError, ConnectionError):
                    pass
                time.sleep(.05)
            else:
                raise AssertionError('isolated binary startup timeout')
            administrator = json.loads((temp / 'data/secrets/credentials.json').read_text())['administrator']
            ticket_request = Request(base + '/admin/browser-tickets', data=b'',
                                     headers={'Authorization': 'Bearer ' + administrator}, method='POST')
            with ADMIN_HTTP.open(ticket_request, timeout=8) as response:
                ticket = json.load(response)['ticket']
            code, login = admin(base, '/admin/session', 'POST', {'ticket': ticket})
            check(code == 200, 'isolated administrator login failed')
            session = login['session_token']
            sources = {}
            for native in ('responses', 'chat_completions', 'messages', 'gemini'):
                provider = 'anthropic' if native == 'messages' else 'gemini' if native == 'gemini' else 'openai'
                code, account = admin(base, '/admin/accounts', 'POST', {'provider': provider,
                    'auth_type': 'api_key', 'name': 'SDK synthetic ' + native}, session)
                check(code == 201, 'synthetic account creation failed: ' + str(account))
                code, _ = admin(base, '/admin/accounts/' + account['id'] + '/credential', 'POST',
                                {'version': account['version'], 'secret': SOURCE_SECRET}, session)
                check(code == 200, 'synthetic credential publication failed')
                code, source = admin(base, '/admin/sources', 'POST', {
                    'name': 'SDK synthetic ' + native, 'account_id': account['id'],
                    'base_url': provider_base + ('/v1beta' if native == 'gemini' else '/v1'),
                    'models': [MODEL], 'native_protocol': native, 'native_operations': ['generate', 'count_tokens'],
                    'proxy_url': '', 'price': {'currency': 'USD', 'input_per_million': '1', 'output_per_million': '1'},
                }, session)
                check(code == 201, 'synthetic source creation failed: ' + str(source))
                sources[native] = source
            directions = [(client, 'gemini') for client in ('responses', 'chat_completions', 'messages')] + [
                ('gemini', native) for native in ('responses', 'chat_completions', 'messages')]
            for client_protocol, native in directions:
                code, key = admin(base, '/admin/client-keys', 'POST', {
                    'name': 'SDK ' + client_protocol + ' to ' + native, 'source_id': sources[native]['id'],
                }, session)
                check(code == 201, 'synthetic client key creation failed')
                sdk = SDKClient(client_protocol, base, key['secret'])
                sdk_clients.append(sdk)
                for mode in ('text', 'stream', 'tool', 'cancel', 'unsupported'):
                    run_case(report, state, sdk, native, mode, base, session, key['key']['id'])
            # Authentication errors must be SDK typed and must not dispatch upstream.
            for protocol in ('responses', 'chat_completions', 'messages', 'gemini'):
                invalid = SDKClient(protocol, base, 'sdk-deliberately-invalid-key')
                sdk_clients.append(invalid)
                state.select('gemini', 'text')
                case = {'direction': protocol + '->local_auth', 'case': 'typed_authentication_error', 'passed': False}
                try:
                    invalid.create()
                    raise AssertionError('invalid SDK key accepted')
                except (openai.APIStatusError, anthropic.APIStatusError, genai_errors.APIError) as error:
                    code = getattr(error, 'status_code', getattr(error, 'code', None))
                    case.update(passed=code == 401 and not state.calls, sdk_error_type=type(error).__name__, http_status=code,
                                native_calls=len(state.calls))
                report['cases'].append(case)
        except Exception as error:
            report['setup_error'] = {'type': type(error).__name__, 'message': str(error)[:1500]}
        finally:
            for sdk in sdk_clients:
                sdk.close()
            if proc is not None:
                if proc.poll() is None:
                    proc.terminate()
                    try:
                        proc.wait(timeout=8)
                    except subprocess.TimeoutExpired:
                        proc.kill()
                        proc.wait(timeout=5)
                proc.stderr.close()
            server.shutdown()
            server.server_close()
    report['passed'] = bool(report['cases']) and 'setup_error' not in report and all(c['passed'] for c in report['cases'])
    report['passed_cases'] = sum(c['passed'] for c in report['cases'])
    report['total_cases'] = len(report['cases'])
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2, ensure_ascii=False) + '\n')
    print(json.dumps({k: report[k] for k in ('passed', 'passed_cases', 'total_cases', 'sdk_versions')}, ensure_ascii=False))
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())

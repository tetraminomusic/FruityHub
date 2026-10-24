"""Какие русские фразы интерфейса (web/index.html) ещё не переведены в web/i18n_en.js.

Запуск из корня репозитория:  python3 scripts/i18n_check.py
"""
import re,sys,json,os
root=os.path.join(os.path.dirname(os.path.abspath(__file__)),'..')
s=open(os.path.join(root,'web','index.html'),encoding='utf-8').read()
html_part=s[s.index('<body>'):s.index('<script>')]
js=s[s.index('<script>')+8:s.rindex('</script>')]
frags=[]
def addtext(t):
    # split html text by tags and entities
    for p in re.split(r'<[^>]*>|&[a-z]+;|&#\d+;',t):
        for q in re.split(r'\n',p):
            q=q.strip()
            if re.search('[А-Яа-яЁё]',q): frags.append(q)
def attrs(t):
    for m in re.finditer(r'(?:title|placeholder|alt)="([^"$]*)"',t):
        if re.search('[А-Яа-яЁё]',m.group(1)): frags.append(m.group(1).strip())
addtext(html_part); attrs(html_part)
# JS tokenizer
i=0;n=len(js)
def read_template(i):
    # js[i]=='`'; returns (end_index, list_of_static_pieces)
    i+=1; buf=''; pieces=[]
    while i<n:
        c=js[i]
        if c=='\\': buf+=js[i:i+2]; i+=2; continue
        if c=='`': pieces.append(buf); return i+1,pieces
        if c=='$' and js[i+1]=='{':
            pieces.append(buf); buf=''
            i=skip_expr(i+2)
            continue
        buf+=c; i+=1
    return i,pieces
def skip_expr(i):
    depth=1
    while i<n and depth>0:
        c=js[i]
        if c in '\'"':
            j,b=read_str(i); handle(b); i=j; continue
        if c=='`':
            j,p=read_template(i); 
            for x in p: handle(x)
            i=j; continue
        if c=='{': depth+=1
        elif c=='}': depth-=1
        i+=1
    return i
def read_str(i):
    q=js[i]; i+=1; buf=''
    while i<n and js[i]!=q:
        if js[i]=='\\': buf+=js[i:i+2]; i+=2; continue
        buf+=js[i]; i+=1
    return i+1,buf
def handle(x):
    x=x.replace("\\'","'").replace('\\n','\n')
    addtext(x); attrs(x)
while i<n:
    c=js[i]
    if js.startswith('//',i) and (i==0 or js[i-1] in ' \n\t;{}(),'):
        j=js.find('\n',i); i=n if j<0 else j; continue
    if js.startswith('/*',i):
        i=js.find('*/',i)+2; continue
    if c in '\'"':
        j,b=read_str(i); handle(b); i=j; continue
    if c=='`':
        j,p=read_template(i)
        for x in p: handle(x)
        i=j; continue
    if c=='/' and re.match(r'[=(,:!&|?{};\[]\s*$', js[max(0,i-3):i].rstrip()[-1:] or ';'):
        # regex literal
        j=i+1
        while j<n and js[j]!='/' :
            if js[j]=='\\': j+=1
            if js[j]=='[':
                while js[j]!=']': j+=1
            j+=1
        i=j+1; continue
    i+=1
seen=[];ss=set()
for f in frags:
    for p in ([f] if not re.search(r'data-|class=|onclick|="|\$|\{|\}|<|"',f) else re.split(r'"|<[^>]*>|>|<|\(|\)',f)):
        p=p.lstrip('>').strip().strip("'").strip()
        if re.search('[А-Яа-яЁё]',p) and not re.search(r'[{}=]',p) and p not in ss:
            ss.add(p); seen.append(p)
js_dict=open(os.path.join(root,'web','i18n_en.js'),encoding='utf-8').read()
d=json.loads(js_dict[js_dict.index('{'):js_dict.rindex('}')+1])
miss=[x for x in seen if x not in d]
for x in miss: print(x)
print(f'не переведено: {len(miss)} из {len(seen)}', file=sys.stderr)
sys.exit(1 if miss else 0)

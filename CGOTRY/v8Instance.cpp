#include "v8Instance.hpp"

v8::MaybeLocal<v8::String> ReadFile(v8::Isolate* isolate, const char* name) {
    FILE* file = fopen(name, "rb");
    if (file == NULL) return v8::MaybeLocal<v8::String>();
    
    fseek(file, 0, SEEK_END);
    size_t size = ftell(file);
    rewind(file);
    
    char* chars = new char[size + 1];
    chars[size] = '\0';
    for (size_t i = 0; i < size;) {
        i += fread(&chars[i], 1, size - i, file);
        if (ferror(file)) {
            fclose(file);
            return v8::MaybeLocal<v8::String>();
        }
    }
    fclose(file);
    v8::MaybeLocal<v8::String> result = v8::String::NewFromUtf8(isolate, chars, v8::NewStringType::kNormal, static_cast<int>(size));
    delete[] chars;
    return result;
}

void Generate(v8::Local<v8::Value> value,int* typeArray,ValueForType* ValArray,v8::Isolate* isolate,int& index,int& Vindex){
    if(value->IsString()){
        typeArray[index++]=STRING;
        v8::String::Utf8Value const strResult(value);
        ValArray[Vindex++].stringValue=std::string(*strResult, strResult.length());
        return;
    }
    
    if(value->IsNumber()){
        if(value->IsInt32() || value->IsUint32()){
            typeArray[index++]=INTNUMBER;
            ValArray[Vindex++].intValue=value->IntegerValue();
        }else{
            typeArray[index++]=FLOATNUMBER;
            ValArray[Vindex++].doubleValue=value->NumberValue();
        }
        return;
    }
    
    if(value->IsBoolean()){
        if(value->IsTrue()){
            typeArray[index++]=BOOLEANTRUE;
        }else{
            typeArray[index++]=BOOLEANFALSE;
        }
        return;
    }
    
    if(value->IsArray()){
        typeArray[index++]=ARRAYSTART;
        v8::Handle<v8::Array> array = v8::Handle<v8::Array>::Cast(value);
        for(int i=0;i<array->Length();i++){
            Generate(array->Get(i),typeArray,ValArray,isolate,index,Vindex);
        }
        typeArray[index++]=ARRAYEND;
        return;
    }
    
    if(value->IsMap()){
        typeArray[index++]=MAPSTART;
        v8::Local<v8::Map> map = v8::Local<v8::Map>::Cast(value);
        v8::Local<v8::Array> array = map->AsArray();
        ValArray[Vindex++].intValue=value->Int32Value();
        for(int i=0;i<array->Length();i++){
            Generate(array->Get(i),typeArray,ValArray,isolate,index,Vindex);
        }
        typeArray[index++]=MAPEND;
        return;
    }

    if(value->IsUndefined() || value->IsNull()){
        typeArray[index++]=UNDEFINED;
        return;
    }
    
    if(value->IsObject()){
        typeArray[index++]=JSONSTRING;
        v8::Local<v8::Object> json = isolate->GetCurrentContext()->Global()->Get(v8::String::NewFromUtf8(isolate, "JSON"))->ToObject();
        v8::Local<v8::Function> stringify = json->Get(v8::String::NewFromUtf8(isolate, "stringify")).As<v8::Function>();
        v8::Local<v8::Value> result;
        result = stringify->Call(json, 1, &value);
        
        v8::String::Utf8Value const strResult(result);
        ValArray[Vindex++].stringValue=std::string(*strResult, strResult.length());
    }
}

void Emit(const v8::FunctionCallbackInfo<v8::Value>& args){
        auto isolate=args.GetIsolate();
        auto x = (Data *)isolate->GetData(0);
        int index=0,vindex=0;
        for(int i=0;i<args.Length();i++){
            Generate(args[i],x->Rmsg->type,x->Rmsg->arr,isolate,index,vindex);
        }
        x->Rmsg->length=index;
}

v8Instance::v8Instance(v8::Platform *platform){
    v8::Isolate::CreateParams create_params;
    create_params.array_buffer_allocator = v8::ArrayBuffer::Allocator::NewDefaultAllocator();
    isolate_ = v8::Isolate::New(create_params);
    v8::Locker locker(GetIsolate());
    v8::Isolate::Scope isolate_scope(GetIsolate());
    v8::HandleScope handle_scope(GetIsolate());
    isolate_->SetData(0, &data);
    data.Rmsg=new msg_response();
    v8::Local<v8::ObjectTemplate> global = v8::ObjectTemplate::New(GetIsolate());
    global->Set(v8::String::NewFromUtf8(GetIsolate(), "emit"),v8::FunctionTemplate::New(GetIsolate(), Emit));
    auto context = v8::Context::New(GetIsolate(), nullptr, global);
    context_.Reset(GetIsolate(), context);
}

v8Instance::~v8Instance(){
    context_.Reset();

}

int v8Instance::v8WorkLoad(std::string jsFile){
    v8::Locker locker(isolate_);
    v8::Isolate::Scope isolate_scope(isolate_);
    v8::HandleScope handle_scope(isolate_);
    
    auto context = context_.Get(isolate_);
    v8::Context::Scope context_scope(context);
    v8::Local<v8::String> file_name = v8::String::NewFromUtf8(GetIsolate(), jsFile.c_str(), v8::NewStringType::kNormal).ToLocalChecked();
    v8::Local<v8::String> source;
    if(!ReadFile(GetIsolate(),jsFile.c_str()).ToLocal(&source)){
    }
    
    if(!ExecuteScript(source,file_name)){
        //Error in Compiling
        std::cerr<<"COMPILATION ERROR\n";
    }
    
    v8::Local<v8::String> on_map = v8::String::NewFromUtf8(isolate_, "OnMap", v8::NewStringType::kNormal).ToLocalChecked();
    auto onMapDef = context->Global()->Get(on_map);
    if (onMapDef->IsFunction()){
        v8::Local<v8::Function> on_map_def = v8::Local<v8::Function>::Cast(onMapDef);
        on_map_[jsFile].Reset(isolate_, on_map_def);
        return 1;
    }
    return 0;
}

bool v8Instance::ExecuteScript(v8::Local<v8::String> source,v8::Local<v8::String> name){
    v8::HandleScope handle_scope(GetIsolate());
    v8::TryCatch try_catch(GetIsolate());
    
    auto context = context_.Get(GetIsolate());
    v8::ScriptOrigin origin(name);
    
    v8::Local<v8::Script> compiled_script;
    if (!v8::Script::Compile(context, source, &origin).ToLocal(&compiled_script)) {
        std::cerr<<"Compilation ERROR";
        return false;
    }
    
    v8::Local<v8::Value> result;
    if (!compiled_script->Run(context).ToLocal(&result)) {
        return false;
    }
    return true;
}

v8::Handle<v8::Object> v8Instance::ParseString(metaData meta){
    v8::Handle<v8::Object> Meta=v8::Object::New(GetIsolate());
    Meta->Set(v8::String::NewFromUtf8(GetIsolate(),"id"), v8::String::NewFromUtf8(GetIsolate(),meta.id));
    Meta->Set(v8::String::NewFromUtf8(GetIsolate(),"cas"), v8::Number::New(GetIsolate(), meta.cas));
    Meta->Set(v8::String::NewFromUtf8(GetIsolate(),"expiration"), v8::Number::New(GetIsolate(),meta.expiration));
    Meta->Set(v8::String::NewFromUtf8(GetIsolate(),"flags"), v8::Number::New(GetIsolate(),meta.flags));
    Meta->Set(v8::String::NewFromUtf8(GetIsolate(),"nru"), v8::Number::New(GetIsolate(),meta.nru));
    Meta->Set(v8::String::NewFromUtf8(GetIsolate(),"byseqno"), v8::Number::New(GetIsolate(), meta.byseqno));
    Meta->Set(v8::String::NewFromUtf8(GetIsolate(),"locktime"), v8::Number::New(GetIsolate(),meta.locktime));
    Meta->Set(v8::String::NewFromUtf8(GetIsolate(),"revseqno"), v8::Number::New(GetIsolate(), meta.revseqno));
    return Meta;
}

msg_response* v8Instance::Map(metaData meta,const char* doc,std::string jsFile){
    v8::Locker locker(GetIsolate());
    v8::Isolate::Scope isolate_scope(GetIsolate());
    v8::HandleScope handle_scope(GetIsolate());
    auto context = context_.Get(GetIsolate());
    v8::Context::Scope context_scope(context);
    v8::TryCatch try_catch(GetIsolate());
    auto x = (Data *)GetIsolate()->GetData(0);
    args[0]= ParseString(meta);
    args[1] = v8::JSON::Parse(v8::String::NewFromUtf8(GetIsolate(), doc));
    auto map = on_map_[jsFile].Get(GetIsolate());
    x->Rmsg->ValueLength=0;
    x->Rmsg->length=0;
    map->Call(context->Global(), 2, args);
    if (try_catch.HasCaught()){
        std::cerr<<"Error in Running\n";
    }
    return x->Rmsg;
}




